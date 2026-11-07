#!/bin/bash

# Arreglo de tamaños de problema (--size) a evaluar.
P_VALUES=(2 4 8 16 32 64 128 256 512 1024 2048 4096 8192 16384 32768 65536 131072 262144 524288 1048576)

# Un CPU lógico por core físico: si dos jobs caen en hermanos SMT comparten L1/L2 y ensucian la curva.
# (En un Ryzen 5 1600 los CPUs 0 y 1 son el mismo core físico, 2 y 3 también, etc.)
export PHYS_CPUS=$(lscpu -p=CPU,CORE | grep -v '^#' | sort -t, -k2,2n -u | cut -d, -f1 | tr '\n' ' ')
JOBS=$(wc -w <<< "$PHYS_CPUS")

# Función que ejecutará GNU parallel
run_sim() {
    local p=$1
    local slot=$2

    # El slot de parallel arranca en 1; lo usamos como índice en la lista de cores físicos
    local cpus=($PHYS_CPUS)
    local core=${cpus[$((slot - 1))]}

    # Ejecuta el binario forzando 1 thread (OMP_NUM_THREADS=1), pineado a un core específico con taskset
    output=$(taskset -c "$core" env OMP_NUM_THREADS=1 ./simuleta --disable-checkpoints --duration 5 --size "$p" 2>&1)

    # Extraer "Operaciones por Segundo (OPS): 1.23 G" y pasarlo a número plano (1230000000)
    # ops=$(echo "$output" | grep "Operaciones por Segundo" | cut -d' ' -f5,6)
    ops=$(echo "$output" | grep "Operaciones por Segundo" | awk -F': ' '{
        split($2, a, " ")
        mult = (a[2] == "G") ? 1e9 : (a[2] == "M") ? 1e6 : (a[2] == "K") ? 1e3 : 1
        printf "%.0f", a[1] * mult
    }')

    # Imprimir resultado en formato CSV
    echo "$p,$ops"
}

# Exportar la función para que 'parallel' pueda verla en las subshells
export -f run_sim

# Pasar los valores de P a parallel.
# -j $JOBS : un trabajo por core físico
# --keep-order : mantiene el orden de salida igual al de entrada
# {%} es el slot de parallel (de 1 a $JOBS)
{
    echo "size,ops"
    printf "%s\n" "${P_VALUES[@]}" | parallel -j "$JOBS" --keep-order run_sim {} {%}
} | tee results.csv
