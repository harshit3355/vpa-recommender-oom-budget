## design set: 3666 containers, 51201 scored container-days

| recommender | OOM kills per 1,000 container-days | container-days with an OOM per 1,000 | memory allocated / peak usage | resizes per container-day |
|---|---:|---:|---:|---:|
| vpa p90 margin 15% (default) | 1176.17 | 77.73 | 1.444 | 0.25 |
| vpa as shipped (250 MiB floor) (default) | 68.44 | 17.73 | 1.475 | 0.20 |
| krr simple buffer 15% (default) | 19.49 | 16.03 | 1.516 | 0.03 |
| krr simple buffer 30% | 11.97 | 9.92 | 1.715 | 0.04 |
| krr simple buffer 50% | 6.48 | 5.68 | 1.985 | 0.04 |
| krr simple buffer 100% | 1.89 | 1.72 | 2.655 | 0.04 |
| budget 0.03/day, base 24h, step scale | 32.50 | 24.57 | 2.121 | 0.60 |
| budget 0.01/day, base 24h, step scale | 11.70 | 8.98 | 3.296 | 0.56 |
| budget 0.003/day, base 24h, step scale | 3.16 | 2.91 | 7.561 | 0.95 |
| budget 0.001/day, base 24h, step scale | 1.21 | 1.21 | 17.099 | 1.08 |
| budget 0.03/day, base 24h, spread scale | 31.93 | 26.25 | 2.763 | 0.57 |
| budget 0.01/day, base 24h, spread scale | 10.64 | 8.67 | 4.912 | 0.61 |
| budget 0.003/day, base 24h, spread scale | 3.77 | 2.97 | 7.968 | 1.15 |
| budget 0.001/day, base 24h, spread scale | 1.04 | 0.92 | 16.916 | 1.60 |
| budget 0.03/day, base 24h, relative scale | 32.40 | 24.49 | 1.797 | 0.55 |
| budget 0.01/day, base 24h, relative scale | 11.74 | 9.04 | 2.566 | 0.64 |
| budget 0.003/day, base 24h, relative scale | 3.32 | 3.03 | 5.326 | 1.07 |
| budget 0.001/day, base 24h, relative scale | 1.21 | 1.19 | 11.578 | 1.39 |
| budget 0.03/day, base 168h, step scale | 27.97 | 24.47 | 1.537 | 0.18 |
| budget 0.01/day, base 168h, step scale | 9.08 | 8.57 | 2.099 | 0.36 |
| budget 0.003/day, base 168h, step scale | 2.97 | 2.79 | 3.056 | 0.58 |
| budget 0.001/day, base 168h, step scale | 1.05 | 1.00 | 4.761 | 1.29 |
| budget 0.03/day, base 168h, spread scale | 30.92 | 25.55 | 1.643 | 0.36 |
| budget 0.01/day, base 168h, spread scale | 9.73 | 9.02 | 3.413 | 1.07 |
| budget 0.003/day, base 168h, spread scale | 2.73 | 2.60 | 6.534 | 1.01 |
| budget 0.001/day, base 168h, spread scale | 0.94 | 0.88 | 11.647 | 1.39 |
| budget 0.03/day, base 168h, relative scale | 26.97 | 23.85 | 1.473 | 0.15 |
| budget 0.01/day, base 168h, relative scale | 9.08 | 8.57 | 1.845 | 0.37 |
| budget 0.003/day, base 168h, relative scale | 2.97 | 2.79 | 2.485 | 0.37 |
| budget 0.001/day, base 168h, relative scale | 1.07 | 1.02 | 3.717 | 1.14 |

Matched at equal or lower OOM rate:

| budget setting | OOM kills per 1,000 container-days | vs cheapest VPA that is as safe | vs cheapest KRR that is as safe |
|---|---:|---|---|

| shipped default | OOM kills per 1,000 container-days | cheapest budget setting that is as safe |
|---|---:|---|
| vpa p90 margin 15% | 1176.17 | none of the budget settings is this safe |
| vpa as shipped (250 MiB floor) | 68.44 | none of the budget settings is this safe |
| krr simple buffer 15% | 19.49 | none of the budget settings is this safe |

Calibration (budget asked for vs OOM kills that happened):

| budget, OOM kills per container-day | realized |
|---:|---:|

