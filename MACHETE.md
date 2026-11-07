# Machete de la demo

## Antes de la charla (en la máquina de la demo)

```bash
nproc                 # cuántos cores → N
lscpu -C              # tamaños de L1d / L2 / L3 (ONE-SIZE = por core)
df -h /dev/shm        # que tenga > N × 50 MB libres (en Docker suele ser 64 MB!)
./simuleta --duration 5   # medir la línea base de ESTA máquina
```

**Cuánto vale `--size` para cada caché:** `size = bytes / 8`. Elegí un `size` que entre holgado en la L1d:

| Caché | Ejemplo 32 KB L1d / 512 KB L2 | `size` que entra |
|-------|-------------------------------|---------------|
| L1d   | 32 KB                         | ≤ 4096 → usar **2048** |
| L2    | 512 KB                        | ≤ 65536 |
| L3    | 8 MB                          | ≤ 1048576 |

## Los pasos (de peor a mejor)

Ratios medidos en un Ryzen 5 1600 de 6 cores y 12 threads; en otra máquina cambian los números, no la tendencia.

| # | Comando | Qué se arregla | OPS (relativo) |
|---|---------|----------------|----------------|
| 1 | `./simuleta` | nada (línea base) | 1× |
| 2 | `./simuleta --workers $(nproc)` | **trampa:** no mejora, incluso empeora | ~0.8× |
| 3 | `OMP_NUM_THREADS=$(nproc) ./simuleta --workers $(nproc)` | puede usar todos los cores | ~5.5× |
| 4 | `… --checkpoint-dir /dev/shm --checkpoint-interval 5` | checkpoints a RAM y menos seguido | ~10× |
| 5 | `… --size 2048` | el problema entra en L1 | ~17× |

Comando final:

```bash
OMP_NUM_THREADS=$(nproc) ./simuleta --workers $(nproc) --checkpoint-dir /dev/shm --checkpoint-interval 5 --size 2048 --duration 20
```

## Qué mostrar en cada paso

**Paso 2 → 3 (la variable de entorno es `OMP_NUM_THREADS`)**
- `htop`: un solo core al 100% aunque haya N workers. Con `H` se ven varios threads, pero solo uno trabaja.
- Para "descubrir" la variable en vivo: `./simuleta -v --workers $(nproc)`. Al arrancar imprime:
  ```
  Threads: 1 (OMP_NUM_THREADS no definida, usando el valor por defecto)
  ```
- Extra para curiosos (revela que es Go): `GODEBUG=schedtrace=1000 ./simuleta --workers $(nproc)`. Ver abajo cómo leerlo.

### Cómo leer `GODEBUG=schedtrace=1000`

Go no le da un thread del SO a cada worker (goroutine). Tiene **P** ("procesadores" de Go, uno por core que puede usar) y reparte las goroutines entre ellos. Cada segundo imprime una línea así:

Sin `OMP_NUM_THREADS` (mal):
```
SCHED 1005ms: gomaxprocs=1 idleprocs=0 threads=5 ... runqueue=1 [ 10 ] ...
```
Con `OMP_NUM_THREADS=12` (bien):
```
SCHED 1009ms: gomaxprocs=12 idleprocs=0 threads=13 ... runqueue=0 [ 0 0 0 0 0 0 0 0 0 0 0 0 ] ...
```

| Campo | Qué significa | Mal → Bien |
|-------|---------------|------------|
| `gomaxprocs` | cuántos P hay = cuántas goroutines pueden correr **a la vez** | 1 → 12 |
| `idleprocs` | P sin trabajo | 0 en ambos (todos ocupados) |
| `threads` | threads del SO (los que ves en `htop` con `H`) | 5 → 13 |
| `runqueue` | goroutines esperando en la cola global | 1 → 0 |
| `[ ... ]` | goroutines esperando en la cola de **cada** P (un número por P) | `[ 10 ]` → todos 0 |

Lectura para el público: *"hay 12 workers, pero un solo carril: 1 corre y 11 hacen fila (10 + 1). Con 12 carriles no hay fila."*

Detalle curioso: la **primera** línea (`SCHED 0ms`) dice `gomaxprocs=12` incluso en el caso malo. Go arranca usando todos los cores y es el programa el que después se limita a 1.

Los demás campos (`spinningthreads`, `needspinning`, `idlethreads`, `schedticks`) se pueden ignorar en la charla.

**Paso 3 → 4 (checkpoints)**
- `strace -f -e signal=none -e trace=openat,fsync -T -p $(pgrep simuleta)`: se ve dónde escribe (`.tmp/checkpoint_worker_*.tmp`) y cuánto tarda cada `fsync`.
- `strace -f -e signal=none -c -p $(pgrep simuleta)` (cortar con Ctrl-C): `fsync` y `write` dominan el tiempo de syscalls.
- El `-e signal=none` oculta los `SIGURG`: es el scheduler de Go interrumpiendo goroutines que llevan más de 10 ms corriendo (preempción). Es normal.
- Si el directorio es NFS (`findmnt -T .` lo dice), `iostat` no lo ve. Usar:
  - `nfsiostat 1`: kB/s escritos y latencia (RTT) por montaje; picos en cada checkpoint, desaparecen con `/dev/shm`.
  - `pidstat -d 1 -p $(pgrep simuleta)`: kB/s escritos por el proceso.
  - `sar -n DEV 1` o `nload`: los checkpoints como ráfagas de red ("el disco es la red").
  - Con NFS el `fsync` espera al servidor: en `strace -T` y en `-vvv` se ve todavía más caro.
- `htop`: todos los cores caen juntos durante el checkpoint (las barreras hacen que todos esperen al más lento).
- `-vv` muestra dónde y cada cuánto escribe los checkpoints, más los logs de las barreras. `-vvv` agrega por worker cuánto tardó cada fase: si `fsync` domina, el problema es el disco; si `barrera1`/`barrera2` son altos, hay workers esperando al más lento.

**Paso 4 → 5 (caché)**
- `lscpu -C` y la tabla de arriba.
- `perf stat -e cache-misses,L1-dcache-load-misses ./simuleta --size 65536 --duration 5` contra `--size 2048` (si `perf` está instalado).
- `./run_benchmark.sh` muestra la curva completa (~20 s).

## Escalado (después del paso 5)

`./run_scaling.sh` corre con 1, 2, 3, 4, 6, 8, 12… hasta N threads y los flags optimizados (~40 s con 12, ~1.5 min con 128). Lo interesante es comparar tamaños:

```bash
./run_scaling.sh                    # comando final: size 2048 (L1), checkpoints a /dev/shm
SIZE=1048576 ./run_scaling.sh       # 8 MB por worker, no cabe en caché
./run_scaling.sh --checkpoint-dir .tmp   # checkpoints a disco en vez de RAM (en NFS: peor)
```

Medido en el Ryzen 5 1600 (6 cores, 12 threads), speedup con 12 threads:

| Caso | Speedup | Eficiencia | Por qué |
|------|---------|------------|---------|
| `size 2048` (L1) | 11.3× | 94% | cada worker trabaja en su caché, casi no comparten nada |
| `size 65536` (L2) | 9.1× | 76% | pasando los 6 threads, los hermanos SMT comparten L2 |
| `size 1048576` (RAM) | 2.7× | 23% | todos compiten por el ancho de banda de memoria |
| checkpoints a disco | 9.6× | 80% | el disco es compartido; en NFS se espera mucho peor |

Puntos para la charla:
- **Más cores no es más rápido si el recurso compartido (memoria, disco, red) es el cuello de botella.**
- Con `size 2048` escala casi lineal **incluso con SMT**: el loop espera la latencia de memoria y mientras un hilo espera, su hermano usa el core. Con código limitado por cómputo, SMT rinde mucho menos.
- Es escalado débil: cada worker trae su propio arreglo, el problema total crece.

## "¿Por qué no usás `--disable-checkpoints`?"

Ya no aparece en `--help`, pero si alguien pregunta:

> En HPC real no podés apagar los checkpoints. Un job que corre días en un cluster puede morir por un nodo caído o por el límite de tiempo de la cola, y sin checkpoint perdés todo. Lo que se hace es escribirlos más rápido (burst buffer, disco local, RAM) y con menos frecuencia, y eso es lo que hacen `--checkpoint-dir` y `--checkpoint-interval`.
