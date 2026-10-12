# Runtime diagnostic logging

Controller result application emits incident opening/closure and terminal action
records after durable commit. The fields identify the monitor incarnation,
incident, action, endpoint and logical delivery where available. Canonical cause
codes accompany failures; provider bodies, configuration values and operator
notes are not copied into those records. A duplicate result does not emit another
committed event record. Snapshot/log restoration and projection loading emit no
live lifecycle records. Retained history remains authoritative, including changes
made outside this result-processing path and records omitted by log sampling.

The shared application logger supplies controller, server, pool and Raft sinks.
Worker-framework messages retain pool identity through the same output rather
than writing a separate stdout prefix. Panic payloads are not printed. Raft
diagnostics retain bounded numeric/boolean fields and safe error codes; arbitrary
framework text fields, error strings, addresses and configuration objects are
discarded. Raft's own static diagnostic message and severity remain available.

Production output includes informational lifecycle records in JSON. Development
console/JSON levels contain no ANSI colouring. Wrapper-specific caller skips
identify application emitters; framework adaptation identifies its sink boundary.
No fallback working-directory log file is created while the structured sink is
active. Diagnostic logs are not a second event journal or evidence of provider
effect. Deployment log rotation and final-source qualification remain separate
requirements.

Real HTTP notification/check processing, duplicate results, actual Raft
snapshot/reopen, production-output/pool attribution and private-field exclusion
have regression coverage. Focused race and minimum-Go checks pass; complete
controller, controller-system, queue and logger suites pass on Go 1.27.1.
The first integration check exposed printf-vet inference in its test and an
incomplete framework interface (Println call sites); both failed checks are
retained separately from the corrected passes. Source review found a typed-nil
standard logger startup regression at the new interface boundary. Constructor
normalization and optional-output guards repair it; the failing reproduction,
complete queue suite, focused race checks and minimum-Go checks are recorded.
Re-review of this correction and final-source qualification remain required.
