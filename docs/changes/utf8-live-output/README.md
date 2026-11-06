# Preserve UTF-8 in live command output

## Reproduction

Verified on Linux with Go 1.27.3 against main `9d5114c14a988966f9c68865c91338a42962f8bf`, 2026-11-06 UTC. No external services are required.

`TestCommandOutputUTF8SurvivesTerminalWebSocket` starts the real local server, uses the existing test pairing cookie, connects a real `/ws/term` WebSocket, and runs real shared-PTY and standalone-process executors. Each command emits ASCII, `é`, `中`, or `🌍` one byte at a time, with 40 ms between bytes. Its callback enters the production `publishTerminalAgent` JSON serializer and WebSocket transport. The test compares decoded live events with the actual captured command output.

Example command for the Chinese character:

```sh
printf '[begin]'; printf '\344'; sleep 0.04; printf '\270'; sleep 0.04; printf '\255'; sleep 0.04; printf '[end]\n'
```

On unchanged main, both executors return exit code 0 and capture `[begin]中[end]`, but live JSON events contain `[begin]���[end]`. The equivalent two-byte and four-byte cases also fail. The two ASCII controls pass. With this fix, all eight cases pass.

To reproduce the original defect without applying the fix, copy only `client/internal/server/unicode_stream_test.go` from this branch into an isolated checkout of the base commit, then run:

```sh
cd client
go test ./internal/server -run '^TestCommandOutputUTF8SurvivesTerminalWebSocket$' -count=1 -v
```

## Cause and fix

Process pipe and PTY read boundaries are byte boundaries, not character boundaries. Converting each chunk directly to a JSON string makes `encoding/json` replace an incomplete UTF-8 character with U+FFFD. Later bytes cannot repair those already-sent events.

The shared `outputstream.Stream` keeps at most three incomplete bytes between callbacks, emits complete valid UTF-8 immediately, and flushes a remaining incomplete suffix as a replacement character on completion. Emission and closing are serialized. The decoder belongs to one command, ignores late callbacks after closing, and does not modify raw output capture. The Windows shared-shell whole-result callback is unchanged.

This concerns live command output, not `read_file` pagination in PR #9. Invalid non-UTF-8 binary output is still replacement text, not a new binary transport. ANSI escape sequences split across reads and pre-existing output-capture/truncation behavior are not changed.

## Verification and evidence

- [Original full regression output](tests-before.txt): six Unicode failures, two ASCII controls pass.
- [Fixed decoder and integration output](tests-after.txt): five decoder tests and all eight process/WebSocket cases pass.
- [Race and regression output](race.txt): decoder, eight Unicode/ASCII cases, shared-shell state/restoration/cancellation, relative-file behavior, and existing terminal-WebSocket test, repeated five times with `-race`, pass.
- [Static and cross-compile output](compile.txt): `go vet` for the three affected packages and Windows amd64 test-binary compilation for agent/server pass. Windows execution was not tested.
- [Cancellation output](cancellation.txt): real shared/standalone process cancellation and context timeout after an incomplete UTF-8 character, repeated three times under `-race`, pass. Callbacks stay valid, flush before return, and do not leak pending bytes into the next command. The shared-shell case handles SIGINT cooperatively; the existing uncooperative-command terminal-close fallback is not changed.
- [Before excerpt](before.txt) and [after excerpt](after.txt) contain exact observation lines from the full logs for screenshot readability.

The integration uses actual production executor and terminal serialization/transport code. The event envelope and call ID are supplied by the test rather than a model-driven agent request. It is not a browser UI test or a complete deployment. No model API, Cloudflare tunnel, remote deployment, or MachineInfo/network-probe path was run. The full repository suite was not run.

The base main was rechecked before publication. Existing PR #2 also changes the two executor files and removes `publishTerminalAgent`; its changes are not included here. Merging #2 first will require rebasing this fix and adapting the integration test to the new event route. No conflict-free combination with that unmerged branch is claimed.
