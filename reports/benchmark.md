## held-out test set: 3179 containers, 44398 scored container-days

| recommender | OOM kills per 1,000 container-days | container-days with an OOM per 1,000 | memory allocated / peak usage | resizes per container-day |
|---|---:|---:|---:|---:|
| vpa p90 margin 15% (default) | 38.22 | 34.96 | 1.448 | 0.28 |
| vpa p90 margin 30% | 22.73 | 21.13 | 1.632 | 0.29 |
| vpa p90 margin 50% | 11.24 | 10.61 | 1.883 | 0.30 |
| vpa p90 margin 100% | 4.46 | 4.12 | 2.513 | 0.31 |
| vpa p95 margin 15% | 30.14 | 27.19 | 1.471 | 0.22 |
| vpa p95 margin 30% | 18.11 | 16.67 | 1.658 | 0.23 |
| vpa p95 margin 50% | 9.03 | 8.58 | 1.913 | 0.23 |
| vpa p95 margin 100% | 3.49 | 3.24 | 2.554 | 0.24 |
| vpa p99 margin 15% | 19.84 | 17.75 | 1.507 | 0.15 |
| vpa p99 margin 30% | 12.07 | 11.01 | 1.700 | 0.15 |
| vpa p99 margin 50% | 6.69 | 6.49 | 1.962 | 0.16 |
| vpa p99 margin 100% | 2.46 | 2.34 | 2.620 | 0.16 |
| vpa as shipped (250 MiB floor) (default) | 22.19 | 20.07 | 1.461 | 0.22 |
| krr simple buffer 15% (default) | 16.80 | 12.39 | 1.520 | 0.03 |
| krr simple buffer 30% | 10.54 | 7.75 | 1.717 | 0.03 |
| krr simple buffer 50% | 5.99 | 4.89 | 1.989 | 0.03 |
| krr simple buffer 100% | 2.07 | 1.91 | 2.658 | 0.04 |
| budget 0.1/day | 61.58 | 53.31 | 1.390 | 0.01 |
| budget 0.03/day | 27.19 | 21.71 | 1.431 | 0.02 |
| budget 0.01/day | 10.11 | 8.04 | 1.722 | 0.32 |
| budget 0.003/day | 2.97 | 2.82 | 2.386 | 0.32 |
| budget 0.001/day (default) | 0.68 | 0.61 | 3.633 | 0.68 |
| budget 0.01/day, base 24h | 12.10 | 9.64 | 2.547 | 0.87 |
| budget 0.001/day, base 24h | 1.60 | 1.60 | 9.230 | 1.00 |
| budget 0.01/day, base 7 days (the design of held-out run 1) | 10.92 | 9.30 | 1.815 | 0.29 |
| budget 0.001/day, base 7 days (the design of held-out run 1) | 0.59 | 0.50 | 3.660 | 0.59 |
| budget 0.01/day, step scale | 10.34 | 8.04 | 1.855 | 0.25 |
| budget 0.001/day, step scale | 0.83 | 0.77 | 4.647 | 0.81 |
| budget 0.01/day, spread scale | 10.59 | 7.91 | 2.355 | 0.69 |
| budget 0.001/day, spread scale | 0.88 | 0.77 | 9.796 | 1.28 |
| budget 0.01/day, per-container calibration | 265.15 | 61.65 | 1.449 | 0.44 |
| budget 0.001/day, per-container calibration | 174.33 | 42.34 | 1.550 | 0.56 |

Memory at the same OOM rate (the baseline's sweep, interpolated in log OOM rate between its two nearest settings; negative means the budget recommender needs less):

| budget setting | OOM kills per 1,000 container-days | vs VPA (floor off, best of 12 settings) | vs KRR (best of 4 buffers) |
|---|---:|---:|---:|
| budget 0.1/day | 61.58 | outside its swept range | outside its swept range |
| budget 0.03/day | 27.19 | -3.3% | outside its swept range |
| budget 0.01/day | 10.11 | -9.2% | -0.9% |
| budget 0.003/day | 2.97 | -7.7% | -1.9% |
| budget 0.001/day | 0.68 | outside its swept range | outside its swept range |

| shipped default | OOM kills per 1,000 container-days | memory / peak | budget recommender at the same OOM rate |
|---|---:|---:|---:|
| vpa p90 margin 15% | 38.22 | 1.448 | 1.414 (-2.3%) |
| vpa as shipped (250 MiB floor) | 22.19 | 1.461 | 1.491 (+2.0%) |
| krr simple buffer 15% | 16.80 | 1.520 | 1.573 (+3.5%) |

Calibration (budget asked for vs OOM kills that happened):

| budget, OOM kills per container-day | realized |
|---:|---:|
| 0.1 | 0.0616 |
| 0.03 | 0.0272 |
| 0.01 | 0.0101 |
| 0.003 | 0.0030 |
| 0.001 | 0.0007 |

