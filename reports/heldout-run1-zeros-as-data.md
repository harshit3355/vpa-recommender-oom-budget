# First held-out run (superseded)

This run fed the trace's zero readings to the recommenders as real data. That broke the VPA baseline (a zero first sample in a 24-hour window makes VPA 1.8.1 ignore that window's later peaks), so the benchmark was re-run with zero readings treated as missing. The recommender's design did not change between the runs. Current results: [benchmark.md](benchmark.md).

## held-out test set: 3673 containers, 51298 scored container-days

| recommender | OOM kills per 1,000 container-days | container-days with an OOM per 1,000 | memory allocated / peak usage | resizes per container-day |
|---|---:|---:|---:|---:|
| vpa p90 margin 15% (default) | 1103.85 | 78.50 | 1.449 | 0.25 |
| vpa p90 margin 30% | 1088.20 | 66.18 | 1.634 | 0.26 |
| vpa p90 margin 50% | 1077.38 | 56.80 | 1.885 | 0.26 |
| vpa p90 margin 100% | 1070.99 | 51.13 | 2.516 | 0.27 |
| vpa p95 margin 15% | 1052.25 | 70.59 | 1.473 | 0.19 |
| vpa p95 margin 30% | 1039.47 | 61.13 | 1.660 | 0.20 |
| vpa p95 margin 50% | 1030.60 | 53.84 | 1.916 | 0.21 |
| vpa p95 margin 100% | 1025.33 | 49.16 | 2.557 | 0.21 |
| vpa p99 margin 15% | 953.99 | 59.94 | 1.510 | 0.13 |
| vpa p99 margin 30% | 944.88 | 53.69 | 1.703 | 0.14 |
| vpa p99 margin 50% | 939.19 | 49.40 | 1.966 | 0.14 |
| vpa p99 margin 100% | 935.19 | 45.77 | 2.624 | 0.14 |
| vpa as shipped (250 MiB floor) (default) | 93.53 | 18.81 | 1.480 | 0.19 |
| krr simple buffer 15% (default) | 21.38 | 16.92 | 1.535 | 0.03 |
| krr simple buffer 30% | 13.33 | 10.41 | 1.735 | 0.04 |
| krr simple buffer 50% | 7.17 | 5.93 | 2.010 | 0.04 |
| krr simple buffer 100% | 2.22 | 1.93 | 2.686 | 0.04 |
| budget 0.1/day | 88.44 | 76.45 | 1.366 | 0.06 |
| budget 0.03/day | 28.40 | 24.23 | 1.494 | 0.49 |
| budget 0.01/day | 9.73 | 8.75 | 1.897 | 0.58 |
| budget 0.003/day | 3.29 | 3.00 | 2.514 | 0.37 |
| budget 0.001/day (default) | 0.82 | 0.70 | 3.575 | 0.78 |
| budget 0.01/day, base 24h | 11.19 | 8.99 | 2.570 | 0.70 |
| budget 0.001/day, base 24h | 1.38 | 1.38 | 9.423 | 0.64 |
| budget 0.01/day, step scale | 9.69 | 8.73 | 2.142 | 0.44 |
| budget 0.001/day, step scale | 1.01 | 0.90 | 4.656 | 0.92 |
| budget 0.01/day, spread scale | 9.96 | 8.79 | 3.673 | 1.09 |
| budget 0.001/day, spread scale | 0.80 | 0.64 | 11.629 | 1.28 |
| budget 0.01/day, per-container calibration | 774.70 | 85.40 | 1.486 | 0.59 |
| budget 0.001/day, per-container calibration | 562.75 | 63.37 | 1.595 | 0.70 |

Matched at equal or lower OOM rate:

| budget setting | OOM kills per 1,000 container-days | vs cheapest VPA that is as safe | vs cheapest KRR that is as safe |
|---|---:|---|---|
| budget 0.1/day | 88.44 | none of its settings is this safe | -11.0% memory vs krr simple buffer 15% |
| budget 0.03/day | 28.40 | none of its settings is this safe | -2.7% memory vs krr simple buffer 15% |
| budget 0.01/day | 9.73 | none of its settings is this safe | -5.6% memory vs krr simple buffer 50% |
| budget 0.003/day | 3.29 | none of its settings is this safe | -6.4% memory vs krr simple buffer 100% |
| budget 0.001/day | 0.82 | none of its settings is this safe | none of its settings is this safe |

| shipped default | OOM kills per 1,000 container-days | cheapest budget setting that is as safe |
|---|---:|---|
| vpa p90 margin 15% | 1103.85 | budget 0.1/day at 88.44: -5.8% memory |
| vpa as shipped (250 MiB floor) | 93.53 | budget 0.1/day at 88.44: -7.7% memory |
| krr simple buffer 15% | 21.38 | budget 0.01/day at 9.73: +23.5% memory |

Calibration (budget asked for vs OOM kills that happened):

| budget, OOM kills per container-day | realized |
|---:|---:|
| 0.1 | 0.0884 |
| 0.03 | 0.0284 |
| 0.01 | 0.0097 |
| 0.003 | 0.0033 |
| 0.001 | 0.0008 |

