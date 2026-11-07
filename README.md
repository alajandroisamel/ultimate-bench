# Ultimate Bench

Un simulador HPC escrito en Go para generar cargas de trabajo intensivas de CPU y memoria, ideal para realizar benchmarks y analizar la latencia de caché.

## Compilación

Para compilar el binario del simulador, simplemente ejecuta el siguiente comando en la raíz del proyecto:

```bash
go build -o simuleta main.go
```

## Uso y Flags Importantes

Puedes ejecutar el simulador directamente. Las flags o opciones más importantes (aquellas que realmente afectan el comportamiento de la simulación) son:

- `-duration`: Duración de la simulación en segundos (por defecto `10`).
- `-workers`: Cantidad de rutinas/workers concurrentes a ejecutar (por defecto `1`).
- `-size`: Tamaño del problema o arreglo por iteración. Afecta directamente los saltos de memoria y los posibles *cache misses* (por defecto `65536`, 512 KB: no entra en la L1).
- `-checkpoint-dir`: Directorio donde se guardarán los checkpoints (por defecto `.tmp/` dentro del directorio actual).
- `-checkpoint-interval`: Cada cuántos segundos hacer un checkpoint a disco (por defecto `2`).
- `-v`, `-vv`, `-vvv`: Modo verboso, acumulable (también vale `-v -v`):
  - `-v` (info): Pid y threads (con el valor de `OMP_NUM_THREADS`). El tamaño del problema y la cantidad de workers se muestran siempre.
  - `-vv` (debug): además, la configuración de checkpoints (directorio e intervalo) y los logs de las barreras de sincronización.
  - `-vvv` (trace): además, por cada worker y checkpoint, el tiempo de espera en cada barrera, de `write` y de `fsync`.

  Los niveles están pensados para la demo: `-v` no menciona los checkpoints, para no adelantar ese paso.

El resto de las flags que aparecen en `--help` (`-engine`, `-use-gpu`, `-mesh-size`, etc.) son decorativas: no hacen nada.

### Variables de entorno

- `OMP_NUM_THREADS`: cantidad de threads (cores) que puede usar el simulador. **Si no está definida, el simulador usa 1 solo core**, sin importar cuántos `-workers` se pidan. Internamente se traduce a `GOMAXPROCS`; la variable `GOMAXPROCS` en sí se ignora a propósito.

### Flags ocultas

No aparecen en `--help` a propósito, para que el público no las vea durante la demo:

- `-disable-checkpoints`: deshabilita por completo la escritura de checkpoints. La usa `run_benchmark.sh` para medir solo CPU/memoria.

## Guía de la demo

Ver [MACHETE.md](MACHETE.md): los pasos de la demo, qué herramienta mostrar en cada uno y la respuesta a "¿por qué no deshabilitar los checkpoints?".

Comando ~óptimo:

```bash
OMP_NUM_THREADS=$(nproc) ./simuleta --workers $(nproc) --checkpoint-interval 5 --checkpoint-dir /dev/shm/ --duration 20 --size 2048
```

## Ejecutar el Benchmark

Si quieres correr pruebas automatizadas con distintos tamaños de memoria (`--size`) para ver la caída de rendimiento por las cachés L1/L2/L3, puedes usar el script de bash incluido (requiere GNU parallel):

```bash
./run_benchmark.sh
```

Corre un job por core físico (evita hermanos SMT, que comparten L1/L2), cada uno pineado con `taskset`. Imprime el CSV por pantalla y lo guarda en `results.csv` con columnas `size,ops` (OPS como número plano). Tarda unos 20 segundos en un equipo de 6 cores.

Los jobs en paralelo siguen compartiendo la L3, así que los valores de `--size` grandes (los que superan la L2) salen algo pesimistas.

## Medir el escalado

```bash
./run_scaling.sh
```

Corre el simulador con 1, 2, 3, 4, 6, 8, 12, … hasta `nproc` threads (potencias de 2 y sus intermedios, más la cantidad de cores físicos) (`OMP_NUM_THREADS=N --workers N`), en serie, y guarda en `scaling.csv` las columnas `threads,ops,speedup,efficiency`. Usa los flags del comando final de la demo (`--checkpoint-dir /dev/shm --checkpoint-interval 5 --size 2048`), 5 s por corrida: ~40 s con 12 threads, ~1.5 min con 128.

- Variables: `SIZE`, `DURATION`, `MAX_THREADS`. Ej: `SIZE=1048576 ./run_scaling.sh`.
- Los argumentos extra se pasan al binario después de los de por defecto, así que los pisan. Ej: `./run_scaling.sh --checkpoint-dir .tmp` para escribir los checkpoints a disco en vez de RAM.

Cada worker tiene su propio arreglo, así que es escalado débil: el problema total crece con los workers.
