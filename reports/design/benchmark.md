## design set: 3164 containers, 44193 scored container-days

| recommender | OOM kills per 1,000 container-days | container-days with an OOM per 1,000 | memory allocated / peak usage | resizes per container-day |
|---|---:|---:|---:|---:|
| vpa p90 margin 15% (default) | 37.04 | 34.24 | 1.442 | 0.28 |
| vpa as shipped (250 MiB floor) (default) | 21.00 | 19.35 | 1.455 | 0.22 |
| krr simple buffer 15% (default) | 14.32 | 11.29 | 1.501 | 0.03 |
| krr simple buffer 30% | 8.69 | 6.95 | 1.697 | 0.03 |
| krr simple buffer 50% | 4.82 | 4.23 | 1.965 | 0.04 |
| krr simple buffer 100% | 1.74 | 1.70 | 2.627 | 0.04 |
| budget 0.03/day, base 24h, step scale | 35.12 | 26.93 | 2.110 | 0.98 |
| budget 0.01/day, base 24h, step scale | 12.60 | 9.62 | 3.215 | 0.61 |
| budget 0.003/day, base 24h, step scale | 3.51 | 3.21 | 6.991 | 1.46 |
| budget 0.001/day, base 24h, step scale | 1.24 | 1.24 | 17.845 | 1.23 |
| budget 0.03/day, base 24h, spread scale | 36.29 | 29.57 | 2.661 | 0.65 |
| budget 0.01/day, base 24h, spread scale | 12.02 | 9.82 | 4.781 | 0.77 |
| budget 0.003/day, base 24h, spread scale | 4.07 | 3.19 | 7.430 | 1.17 |
| budget 0.001/day, base 24h, spread scale | 1.02 | 0.93 | 16.212 | 1.79 |
| budget 0.03/day, base 24h, relative scale | 34.69 | 26.75 | 1.784 | 0.87 |
| budget 0.01/day, base 24h, relative scale | 12.60 | 9.71 | 2.501 | 0.67 |
| budget 0.003/day, base 24h, relative scale | 3.60 | 3.30 | 4.975 | 1.29 |
| budget 0.001/day, base 24h, relative scale | 1.15 | 1.15 | 11.866 | 1.23 |
| budget 0.03/day, base 168h, step scale | 32.20 | 26.45 | 1.460 | 0.29 |
| budget 0.01/day, base 168h, step scale | 10.66 | 9.41 | 1.942 | 0.20 |
| budget 0.003/day, base 168h, step scale | 3.03 | 2.81 | 2.931 | 0.62 |
| budget 0.001/day, base 168h, step scale | 0.95 | 0.88 | 4.758 | 0.91 |
| budget 0.03/day, base 168h, spread scale | 32.15 | 26.00 | 1.556 | 0.18 |
| budget 0.01/day, base 168h, spread scale | 11.45 | 9.53 | 2.646 | 0.88 |
| budget 0.003/day, base 168h, spread scale | 3.30 | 2.99 | 5.336 | 1.04 |
| budget 0.001/day, base 168h, spread scale | 1.13 | 1.02 | 9.970 | 1.68 |
| budget 0.03/day, base 168h, relative scale | 31.07 | 26.00 | 1.413 | 0.35 |
| budget 0.01/day, base 168h, relative scale | 10.66 | 9.41 | 1.754 | 0.15 |
| budget 0.003/day, base 168h, relative scale | 3.08 | 2.87 | 2.404 | 0.79 |
| budget 0.001/day, base 168h, relative scale | 0.91 | 0.84 | 3.692 | 0.73 |
| budget 0.03/day, base 336h, relative scale | 25.89 | 22.11 | 1.412 | 0.02 |
| budget 0.01/day, base 336h, relative scale | 9.41 | 7.99 | 1.644 | 0.24 |
| budget 0.003/day, base 336h, relative scale | 2.78 | 2.76 | 2.279 | 0.25 |
| budget 0.001/day, base 336h, relative scale | 1.11 | 1.11 | 3.381 | 0.75 |

Memory at the same OOM rate (the baseline's sweep, interpolated in log OOM rate between its two nearest settings; negative means the budget recommender needs less):

| budget setting | OOM kills per 1,000 container-days | vs VPA (floor off, best of 12 settings) | vs KRR (best of 4 buffers) |
|---|---:|---:|---:|

| shipped default | OOM kills per 1,000 container-days | memory / peak | budget recommender at the same OOM rate |
|---|---:|---:|---:|
| vpa p90 margin 15% | 37.04 | 1.442 | outside its swept range |
| vpa as shipped (250 MiB floor) | 21.00 | 1.455 | outside its swept range |
| krr simple buffer 15% | 14.32 | 1.501 | outside its swept range |

Calibration (budget asked for vs OOM kills that happened):

| budget, OOM kills per container-day | realized |
|---:|---:|

