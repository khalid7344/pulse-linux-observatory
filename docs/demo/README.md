# Genuine terminal capture

`fixture.typescript` is raw output from the running application under a pseudo-terminal,
captured with util-linux `script`. It contains ANSI screen-control bytes, not a
fabricated screenshot. Only checked-in synthetic/reference fixtures were used.

Reproduce after building, at 140 columns by 32 rows:

```sh
script -q -c 'stty cols 140 rows 32; ./bin/pulse --proc-root=testdata/proc_fixtures/tick_01 --interval=250ms' /tmp/fixture.typescript
# Press q to exit.
```

Inspect raw recordings as text with a suitable editor. Control sequences are
expected. The capture proves fixture rendering and clean quit, not live hardware
or broad terminal compatibility. No live process command lines were captured.
