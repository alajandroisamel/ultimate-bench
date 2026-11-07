#!/bin/bash

# Mide cómo escala el simulador con la cantidad de cores: corre con 1, 2, ..., nproc threads
# (OMP_NUM_THREADS=N y --workers N) y calcula speedup y eficiencia respecto de 1 thread.
# Usa los mismos flags que el comando final de la demo (checkpoints a /dev/shm, size en L1).
#
# Cada worker tiene su propio arreglo de --size elementos, así que el problema total crece con
# los workers (escalado débil): lo ideal es que las OPS crezcan linealmente.
#
# Uso:
#   ./run_scaling.sh                          # flags optimizados
#   SIZE=1048576 ./run_scaling.sh             # no entra en caché: compiten por la memoria
#   ./run_scaling.sh --checkpoint-dir .tmp    # argumentos extra pisan a los de por defecto

SIZE=${SIZE:-4096}
DURATION=${DURATION:-5}
MAX_THREADS=${MAX_THREADS:-$(nproc)}

# Van después de los flags por defecto: con el paquete flag de Go gana el último
EXTRA_ARGS=("$@")

# Extraer "Operaciones por Segundo (OPS): 1.23 G" y pasarlo a número plano (1230000000)
parse_ops() {
    grep "Operaciones por Segundo" | awk -F': ' '{
        split($2, a, " ")
        mult = (a[2] == "G") ? 1e9 : (a[2] == "M") ? 1e6 : (a[2] == "K") ? 1e3 : 1
        printf "%.0f", a[1] * mult
    }'
}

# Cantidades de threads a probar: potencias de 2 y sus intermedios ×1.5 (1, 2, 3, 4, 6, 8, 12, ...),
# más los cores físicos y MAX_THREADS, que es donde cambia la curva. Con 128 cores son ~15 corridas
# en vez de 128.
thread_counts() {
    local physical
    physical=$(lscpu -p=CORE | grep -v '^#' | sort -u | wc -l)
    {
        for ((p=1; p<=MAX_THREADS; p*=2)); do
            echo "$p"
            echo $((p * 3 / 2))
        done
        echo "$physical"
        echo "$MAX_THREADS"
    } | awk -v max="$MAX_THREADS" '$1 >= 1 && $1 <= max' | sort -nu
}

# Las corridas van en serie: cada una usa N cores y no pueden pisarse
{
    echo "threads,ops,speedup,efficiency"
    base=""
    for n in $(thread_counts); do
        ops=$(OMP_NUM_THREADS=$n ./simuleta --workers "$n" --duration "$DURATION" --size "$SIZE" \
            --checkpoint-dir /dev/shm --checkpoint-interval 5 "${EXTRA_ARGS[@]}" 2>&1 | parse_ops)
        base=${base:-$ops}
        awk -v n="$n" -v ops="$ops" -v base="$base" \
            'BEGIN { s = ops / base; printf "%d,%d,%.2f,%.2f\n", n, ops, s, s / n }'
    done
} | tee scaling.csv
