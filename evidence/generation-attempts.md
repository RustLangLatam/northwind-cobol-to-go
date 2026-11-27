# Generation attempts refused by the gates

The Milton Prism platform generates each service in an agent run, and accepts the result only after its gates pass. This file lists the attempts that the gates refused, and the attempt that was accepted, for the two services in this repository.

**Source.** These facts come from the platform's generation-worker logs and run records. Those logs and records are not public and are not included in this repository. The statements below are the platform's record of each run. No log is attached.

## Earlier run of the same two services

- **NWC620 failed all 3 attempts.**
  - Attempt 1: the agent wrote files outside its scope. It regenerated the platform's protobuf stubs. The scope check removed those files and refused the attempt.
  - Attempts 2 and 3: the COBOL decimal gate refused a test that decoded a number into an `any` (interface) value, where numbers become `float64`.
- **NWC700 failed attempt 1.** A wrapper was passed to `db2access.ConfigFromEnv` instead of `os.Getenv`. A later retry passed on attempt 1.

## This build

- **NWC700 attempt 1 was refused** by the structural gate, for a `toolchain` line in `go.mod`. **Attempt 2 passed.** The panel records NWC700 as generated on attempt 2.
- **NWC620 passed on attempt 1**, after an API rate-limit pause. The platform rescheduled the attempt.
