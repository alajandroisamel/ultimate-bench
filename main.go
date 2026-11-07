package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const CheckpointSizeBytes = 50 * 1024 * 1024 // 50MB por defecto, ajustable a mano

func main() {
	// Cantidad de threads tomada de OMP_NUM_THREADS (suena a HPC genérico, no a Go).
	// Si no está definida o es inválida, limitarlo a 1. GOMAXPROCS se ignora a propósito.
	ompEnv := os.Getenv("OMP_NUM_THREADS")
	threads, err := strconv.Atoi(ompEnv)
	if err != nil || threads < 1 {
		threads = 1
	}
	runtime.GOMAXPROCS(threads)

	pwd, err := os.Getwd()
	if err != nil {
		pwd = "/tmp"
	}
	pwd = filepath.Join(pwd, ".tmp")

	// Flags
	durationSec := flag.Int("duration", 10, "Duración de la simulación en segundos")
	workers := flag.Int("workers", 1, "Cantidad de workers (goroutines) concurrentes")
	checkpointDir := flag.String("checkpoint-dir", pwd, "Directorio absoluto para escribir checkpoints")
	checkpointInterval := flag.Int("checkpoint-interval", 2, "Cantidad de segundos entre cada checkpoint")
	disableCheckpoints := flag.Bool("disable-checkpoints", false, "Deshabilitar por completo la escritura de checkpoints")
	var verbose verbosity
	flag.Var(&verbose, "v", "Modo verboso, repetible: -v info, -vv debug, -vvv trace")
	p := flag.Int("p", 8192, "Tamaño del problema (elementos del arreglo por iteración)")

	// Opciones falsas (placeholders para dar realismo al help)
	_ = flag.String("engine", "default", "Motor de simulación a utilizar (default, quantum, fluid-dynamics, monte-carlo)")
	_ = flag.Int64("seed", 42, "Semilla para el generador de números pseudoaleatorios")
	_ = flag.String("precision", "float64", "Precisión aritmética (float32, float64, quad)")
	_ = flag.String("mesh-size", "1024x1024x1024", "Dimensiones de la malla computacional")
	_ = flag.Bool("use-gpu", false, "Habilitar aceleración por hardware (CUDA/ROCm/Metal)")
	_ = flag.String("network-topology", "hypercube", "Topología de red simulada (torus, hypercube, fat-tree)")
	_ = flag.Float64("temperature", 298.15, "Temperatura inicial del sistema en Kelvin")
	_ = flag.Bool("enable-telemetry", true, "Habilitar la exportación de métricas a Prometheus/gRPC")
	_ = flag.Float64("cfl-condition", 0.45, "Número de Courant-Friedrichs-Lewy (CFL) máximo permitido")
	_ = flag.String("boundary-conditions", "periodic", "Condiciones de contorno en la malla (periodic, dirichlet, neumann)")
	_ = flag.Bool("debug-nan", false, "Interrumpir la simulación al detectar NaNs o Infs en los tensores")
	_ = flag.Int("log-level", 1, "Nivel de detalle de los logs (0=quiet, 1=info, 2=debug, 3=trace)")

	// Flags ocultas: funcionan pero no aparecen en el help (para que el público no las vea)
	hidden := map[string]bool{"disable-checkpoints": true}
	flag.Usage = func() {
		visible := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
		visible.SetOutput(flag.CommandLine.Output())
		flag.VisitAll(func(f *flag.Flag) {
			if !hidden[f.Name] {
				visible.Var(f.Value, f.Name, f.Usage)
				visible.Lookup(f.Name).DefValue = f.DefValue
			}
		})
		fmt.Fprintf(visible.Output(), "Usage of %s:\n", os.Args[0])
		visible.PrintDefaults()
	}

	flag.CommandLine.Parse(expandVerbosity(os.Args[1:]))

	fmt.Printf("Iniciando simulación\n")
	if verbose >= 1 {
		fmt.Printf("Pid: %d\n", os.Getpid())
		fmt.Printf("Tamaño del problema (p): %d\n", *p)
		fmt.Printf("Cantidad de workers: %d\n", *workers)
		if ompEnv == "" {
			fmt.Printf("Threads: %d (OMP_NUM_THREADS no definida, usando el valor por defecto)\n", threads)
		} else {
			fmt.Printf("Threads: %d (OMP_NUM_THREADS=%s)\n", threads, ompEnv)
		}
	}
	if verbose >= 2 {
		if *disableCheckpoints {
			fmt.Printf("Checkpoints: deshabilitados\n")
		} else {
			fmt.Printf("Checkpoints: cada %ds en %s\n", *checkpointInterval, *checkpointDir)
		}
	}
	fmt.Printf("---\n")

	// Ensure checkpoint directory exists
	if !*disableCheckpoints {
		if err := os.MkdirAll(*checkpointDir, 0755); err != nil {
			log.Fatalf("Error creando directorio de checkpoints: %v", err)
		}
		// Cleanup at start
		cleanupCheckpoints(*checkpointDir, *workers)
	}

	// Setup simulation variables
	var totalOps atomic.Uint64
	var wg sync.WaitGroup
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*durationSec)*time.Second)
	defer cancel()

	barrier := NewHPCBarrier(*workers, ctx)
	var checkpointEpoch atomic.Uint32
	checkpointDone := make(chan struct{}, 1)

	if !*disableCheckpoints {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(*checkpointInterval) * time.Second):
					checkpointEpoch.Add(1)
					// Esperar a que los workers terminen el checkpoint actual
					// antes de empezar a contar el tiempo para el próximo
					select {
					case <-ctx.Done():
						return
					case <-checkpointDone:
					}
				}
			}
		}()
	}

	startTime := time.Now()

	// Launch workers
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go worker(ctx, w, *p, *checkpointDir, *disableCheckpoints, verbose, &totalOps, &wg, barrier, &checkpointEpoch, checkpointDone)
	}

	// Wait for workers to finish
	wg.Wait()
	elapsed := time.Since(startTime)

	// Cleanup at end
	if !*disableCheckpoints {
		cleanupCheckpoints(*checkpointDir, *workers)
	}

	// Final Metrics
	ops := totalOps.Load()
	opsPerSec := float64(ops) / elapsed.Seconds()
	
	fmt.Printf("Simulación finalizada en %v\n", elapsed)
	fmt.Printf("Operaciones por Segundo (OPS): %s\n", formatOPS(opsPerSec))
}

// verbosity es un contador estilo ssh/rsync: cada -v suma un nivel.
type verbosity int

func (v *verbosity) String() string   { return strconv.Itoa(int(*v)) }
func (v *verbosity) IsBoolFlag() bool { return true }
func (v *verbosity) Set(s string) error {
	if s == "true" {
		*v++
		return nil
	}
	n, err := strconv.Atoi(s)
	*v = verbosity(n)
	return err
}

// expandVerbosity traduce -vv / -vvv (que el paquete flag no entiende) a -v=2 / -v=3.
func expandVerbosity(args []string) []string {
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			return append(out, args[i:]...)
		}
		name := strings.TrimLeft(a, "-")
		if len(name) > 1 && len(a) > len(name) && strings.Trim(name, "v") == "" {
			a = fmt.Sprintf("-v=%d", len(name))
		}
		out = append(out, a)
	}
	return out
}

func formatOPS(ops float64) string {
	if ops >= 1e9 {
		return fmt.Sprintf("%.2f G", ops/1e9)
	} else if ops >= 1e6 {
		return fmt.Sprintf("%.2f M", ops/1e6)
	} else if ops >= 1e3 {
		return fmt.Sprintf("%.2f K", ops/1e3)
	}
	return fmt.Sprintf("%.2f", ops)
}

type HPCBarrier struct {
	count int
	total int
	gen   int // Generación de la barrera (evita race conditions en barreras cíclicas)
	mutex sync.Mutex
	cond  *sync.Cond
	ctx   context.Context
}

func NewHPCBarrier(size int, ctx context.Context) *HPCBarrier {
	b := &HPCBarrier{total: size, ctx: ctx}
	b.cond = sync.NewCond(&b.mutex)
	go func() {
		<-ctx.Done()
		b.mutex.Lock()
		b.cond.Broadcast()
		b.mutex.Unlock()
	}()
	return b
}

func (b *HPCBarrier) Wait() bool {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	
	if b.ctx.Err() != nil {
		return false
	}
	
	gen := b.gen
	b.count++
	if b.count == b.total {
		b.count = 0
		b.gen++ // Avanzar a la siguiente generación
		b.cond.Broadcast()
		return true
	}
	
	// Prevenir despertares espurios o que un worker muy rápido pise a uno lento
	for gen == b.gen && b.ctx.Err() == nil {
		b.cond.Wait()
	}
	
	return b.ctx.Err() == nil
}

func cleanupCheckpoints(dir string, workers int) {
	for i := 0; i < workers; i++ {
		filename := filepath.Join(dir, fmt.Sprintf("checkpoint_worker_%d.tmp", i))
		os.Remove(filename)
	}
}

func worker(ctx context.Context, id int, p int, checkpointDir string, disableCheckpoints bool, verbose verbosity, totalOps *atomic.Uint64, wg *sync.WaitGroup, barrier *HPCBarrier, checkpointEpoch *atomic.Uint32, checkpointDone chan struct{}) {
	defer wg.Done()

	// Asignar un arreglo propio para forzar lecturas/escrituras en memoria y evidenciar cache misses
	data := make([]float64, p)
	for i := range data {
		data[i] = rand.Float64()
	}

	iterations := 0
	checkpointFilename := filepath.Join(checkpointDir, fmt.Sprintf("checkpoint_worker_%d.tmp", id))
	localEpoch := uint32(0)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// CPU Intensive work: simulación con acceso pseudoaleatorio dependiente de los datos.
		// Al hacer que el siguiente índice dependa del valor cargado de la memoria en esta iteración,
		// obligamos al procesador a serializar las lecturas (derrotamos el Out-of-Order execution y el Memory Level Parallelism).
		// Esto revelará la latencia real de la caché L1, L2 y L3 de forma escalonada.
		sum := 0.0
		var state uint32 = uint32(id+1) + uint32(iterations) // seed
		idx := 0
		for i := 0; i < p; i++ {
			// Hacer que el PRNG dependa de la lectura ANTERIOR de memoria
			bits := math.Float64bits(data[idx])
			state ^= uint32(bits)
			
			// Fast Xorshift32
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			
			// Lemire's method
			idx = int((uint64(state) * uint64(p)) >> 32)
			
			data[idx] = data[idx]*1.0000001 + 0.0000001
			sum += data[idx]
		}
		
		// Prevenir optimización total del compilador
		_ = sum 

		// Track ops. Cada loop hace `p` operaciones
		totalOps.Add(uint64(p))
		iterations++

		// IO operation (checkpoint)
		if !disableCheckpoints {
			currentEpoch := checkpointEpoch.Load()
			if currentEpoch > localEpoch {
				localEpoch = currentEpoch
				
				if verbose >= 2 {
					fmt.Printf("[Worker %d] Entrando a Barrera 1 (Epoch %d)\n", id, currentEpoch)
				}

				// Sincronizar todos los workers antes de escribir (Barrier 1)
				t0 := time.Now()
				if !barrier.Wait() {
					return
				}
				wait1 := time.Since(t0)

				if verbose >= 2 {
					fmt.Printf("[Worker %d] Barrera 1 superada. Iniciando escritura de Checkpoint...\n", id)
				}

				writeDur, syncDur := writeCheckpoint(checkpointFilename)

				if verbose >= 2 {
					fmt.Printf("[Worker %d] Escritura finalizada. Entrando a Barrera 2\n", id)
				}

				// Sincronizar todos los workers después de escribir para arrancar el cómputo a la vez (Barrier 2)
				t0 = time.Now()
				if !barrier.Wait() {
					return
				}
				wait2 := time.Since(t0)

				if verbose >= 3 {
					fmt.Printf("[Worker %d] Tiempos epoch %d: barrera1=%v write=%v fsync=%v barrera2=%v\n",
						id, currentEpoch, wait1.Round(time.Millisecond), writeDur.Round(time.Millisecond),
						syncDur.Round(time.Millisecond), wait2.Round(time.Millisecond))
				}
				
				// El worker 0 le avisa al coordinador que el checkpoint global ha terminado
				if id == 0 {
					select {
					case checkpointDone <- struct{}{}:
					default:
					}
				}
				
				if verbose >= 2 {
					fmt.Printf("[Worker %d] Barrera 2 superada. Reanudando cómputo...\n", id)
				}
			}
		}
	}
}

// writeCheckpoint devuelve cuánto tardó la escritura y cuánto el fsync (para -vvv)
func writeCheckpoint(filename string) (writeDur, syncDur time.Duration) {
	start := time.Now()
	// Escribir a disco usando file IO, util para observar con strace
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return 0, 0 // ignorar errores para no frenar la simulacion si el disco falla
	}
	defer f.Close()
	
	// Escribir en bloques de 1MB para no saturar memoria RAM
	chunk := make([]byte, 1024*1024)
	for i := range chunk {
		chunk[i] = 1 // Datos no nulos para evitar optimizaciones de sparse files
	}
	
	bytesWritten := 0
	for bytesWritten < CheckpointSizeBytes {
		n, _ := f.Write(chunk)
		bytesWritten += n
	}
	writeDur = time.Since(start)
	start = time.Now()
	f.Sync() // forzar fsync para generar IO wait
	return writeDur, time.Since(start)
}
