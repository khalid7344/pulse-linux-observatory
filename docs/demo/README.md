# Terminal demonstration

`fixture.typescript` records the application running with bundled fixtures.
It contains ANSI terminal control sequences. Record a session after building:

```sh
script -q -c 'stty cols 140 rows 32; ./bin/pulse --proc-root=testdata/proc_fixtures/tick_01 --interval=250ms --enable-pss' /tmp/fixture.typescript
# Press q to exit.
```

The README PNG renders text captured from a running terminal session on a dark
background using DejaVu Sans Mono. Only application output is included.
