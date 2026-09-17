package systemd

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

// recorder captures what would have been run, and replies with what a test
// wants systemd to have said.
type recorder struct {
	calls   [][]string
	replies map[string]string
	fail    error
}

func (r *recorder) runner() Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		r.calls = append(r.calls, append([]string{name}, args...))

		if r.fail != nil {
			return nil, r.fail
		}

		return []byte(r.replies[name]), nil
	}
}

func TestStatusReadsSystemdProperties(t *testing.T) {
	commands := &recorder{replies: map[string]string{"systemctl": strings.Join([]string{
		"ActiveState=active",
		"SubState=running",
		"UnitFileState=enabled",
		"Result=success",
		"ActiveEnterTimestamp=Wed 2026-09-17 08:14:02 UTC",
	}, "\n")}}

	status, err := (&Manager{Run: commands.runner()}).Status(t.Context(), "k0sworker")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	// The bare name is accepted: `cctl logs k0sworker` is what somebody types.
	if status.Name != "k0sworker.service" {
		t.Errorf("Name = %q, want the full unit name", status.Name)
	}

	if status.Active != "active" || status.Sub != "running" || status.Result != "success" {
		t.Errorf("status = %+v", status)
	}

	if !status.Restartable {
		t.Error("k0sworker should be restartable")
	}
}

func TestPropertiesThatSystemdCallsUnsetAreOmitted(t *testing.T) {
	// A timestamp of "n/a" means it never happened. Passing that through would
	// have a client print "n/a" as though it were a time.
	commands := &recorder{replies: map[string]string{"systemctl": strings.Join([]string{
		"ActiveState=inactive",
		"SubState=dead",
		"ActiveEnterTimestamp=n/a",
		"Result=",
	}, "\n")}}

	status, err := (&Manager{Run: commands.runner()}).Status(t.Context(), "corium-uncordon.service")
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}

	if status.Since != "" || status.Result != "" {
		t.Errorf("status = %+v, want the unset fields empty", status)
	}
}

func TestUnknownUnitsAreRefused(t *testing.T) {
	manager := &Manager{Run: (&recorder{}).runner()}

	// The whole point of the allowlist: a name this API does not know never
	// reaches a command line.
	for _, name := range []string{
		"sshd.service",
		"k0sworker.service; rm -rf /",
		"../../../etc/passwd",
		"",
	} {
		if _, err := manager.Status(t.Context(), name); !errors.Is(err, ErrUnknownUnit) {
			t.Errorf("Status(%q) = %v, want %v", name, err, ErrUnknownUnit)
		}

		if _, err := manager.Restart(t.Context(), name); !errors.Is(err, ErrUnknownUnit) {
			t.Errorf("Restart(%q) = %v, want %v", name, err, ErrUnknownUnit)
		}
	}
}

func TestRestartIsRefusedForUnitsThatRunOnce(t *testing.T) {
	// corium-bootstrap re-running on a node that has already joined a cluster
	// is the failure this project guards against everywhere else. It is
	// readable and it is not restartable, and the two are separate lists for
	// exactly this reason.
	commands := &recorder{}
	manager := &Manager{Run: commands.runner()}

	for _, name := range []string{
		"corium-bootstrap.service",
		"corium-upgrade-apply.service",
		"greenboot-healthcheck.service",
		"cloud-final.service",
	} {
		if _, err := manager.Restart(t.Context(), name); !errors.Is(err, ErrNotRestartable) {
			t.Errorf("Restart(%q) = %v, want %v", name, err, ErrNotRestartable)
		}
	}

	// And nothing was run on the way to refusing.
	for _, call := range commands.calls {
		if len(call) > 1 && call[1] == "restart" {
			t.Errorf("a refused restart still ran %v", call)
		}
	}
}

func TestRestartCyclesAndThenReportsTheResult(t *testing.T) {
	commands := &recorder{replies: map[string]string{
		"systemctl": "ActiveState=activating\nSubState=start",
	}}

	status, err := (&Manager{Run: commands.runner()}).Restart(t.Context(), "k0scontroller.service")
	if err != nil {
		t.Fatalf("Restart() error = %v", err)
	}

	if len(commands.calls) != 2 {
		t.Fatalf("made %d calls, want a restart and then a show", len(commands.calls))
	}

	if got := commands.calls[0]; got[1] != "restart" || got[2] != "k0scontroller.service" {
		t.Errorf("first call = %v, want a restart of k0scontroller.service", got)
	}

	// `systemctl restart` returning zero means the job was accepted, not that
	// the service is up, so the reported state is read back rather than
	// assumed.
	if status.Active != "activating" {
		t.Errorf("Active = %q, want the state read back after the restart", status.Active)
	}
}

func TestListCoversEveryKnownUnit(t *testing.T) {
	commands := &recorder{replies: map[string]string{"systemctl": "ActiveState=inactive"}}

	statuses, err := (&Manager{Run: commands.runner()}).List(t.Context())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}

	if len(statuses) != len(known) {
		t.Fatalf("List() returned %d units, want %d", len(statuses), len(known))
	}

	for i := 1; i < len(statuses); i++ {
		if statuses[i-1].Name > statuses[i].Name {
			t.Errorf("List() is not sorted: %q before %q", statuses[i-1].Name, statuses[i].Name)
		}
	}
}

func TestLogArgumentsNeverCarryWhatACallerTyped(t *testing.T) {
	manager := &Manager{}

	arguments, err := manager.journalArguments(LogOptions{Unit: "k0sworker", Lines: 50, Since: "15m"})
	if err != nil {
		t.Fatalf("journalArguments() error = %v", err)
	}

	joined := strings.Join(arguments, " ")

	if !strings.Contains(joined, "--unit k0sworker.service") {
		t.Errorf("arguments = %v, want the resolved unit name", arguments)
	}

	// The duration is rendered as an absolute time, so the string the caller
	// sent never becomes part of a command line.
	if strings.Contains(joined, "15m") {
		t.Errorf("arguments = %v, want the caller's own text absent", arguments)
	}
}

func TestLogLinesAreBounded(t *testing.T) {
	manager := &Manager{}

	for given, want := range map[int]string{
		0:            "--lines " + strconv.Itoa(DefaultLines),
		-1:           "--lines " + strconv.Itoa(DefaultLines),
		50:           "--lines 50",
		MaxLines * 4: "--lines " + strconv.Itoa(MaxLines),
	} {
		arguments, err := manager.journalArguments(LogOptions{Lines: given})
		if err != nil {
			t.Fatalf("journalArguments(%d) error = %v", given, err)
		}

		if !strings.Contains(strings.Join(arguments, " "), want) {
			t.Errorf("Lines=%d gave %v, want %q", given, arguments, want)
		}
	}
}

func TestLogsWithNoUnitStayInsideTheAllowlist(t *testing.T) {
	// "No unit" must mean "everything this API knows about", not "the whole
	// journal" -- otherwise it is a way around the list.
	arguments, err := (&Manager{}).journalArguments(LogOptions{})
	if err != nil {
		t.Fatalf("journalArguments() error = %v", err)
	}

	units := 0

	for i, argument := range arguments {
		if argument == "--unit" {
			units++

			if _, ok := lookup(arguments[i+1]); !ok {
				t.Errorf("arguments name %q, which is not on the allowlist", arguments[i+1])
			}
		}
	}

	if units != len(known) {
		t.Errorf("named %d units, want all %d", units, len(known))
	}
}

func TestKernelLogsAreTheJournalToo(t *testing.T) {
	arguments, err := (&Manager{}).journalArguments(LogOptions{Unit: KernelUnit})
	if err != nil {
		t.Fatalf("journalArguments() error = %v", err)
	}

	if !strings.Contains(strings.Join(arguments, " "), "--dmesg") {
		t.Errorf("arguments = %v, want the kernel's own messages", arguments)
	}
}

func TestBadSinceIsRefused(t *testing.T) {
	for _, since := range []string{"yesterday", "-5m", "0s", "15"} {
		if _, err := (&Manager{}).journalArguments(LogOptions{Since: since}); err == nil {
			t.Errorf("journalArguments(since=%q) = nil, want an error", since)
		}
	}
}

// A journald record, as it actually comes out of `journalctl --output=json`.
const journalLine = `{"__REALTIME_TIMESTAMP":"1789671887000000","_SYSTEMD_UNIT":"k0sworker.service",` +
	`"PRIORITY":"6","MESSAGE":"starting kubelet","_BOOT_ID":"abc","_CAP_EFFECTIVE":"1ffffffffff"}`

func TestLogsEmitOnlyWhatIsWorthSending(t *testing.T) {
	commands := &recorder{replies: map[string]string{"journalctl": journalLine + "\n"}}

	var out strings.Builder
	if err := (&Manager{Run: commands.runner()}).Logs(t.Context(), LogOptions{}, &out); err != nil {
		t.Fatalf("Logs() error = %v", err)
	}

	var record Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &record); err != nil {
		t.Fatalf("output is not one JSON record per line: %v", err)
	}

	if record.Message != "starting kubelet" || record.Unit != "k0sworker.service" {
		t.Errorf("record = %+v", record)
	}

	if want := time.UnixMicro(1789671887000000).UTC(); !record.Time.Equal(want) {
		t.Errorf("Time = %v, want %v", record.Time, want)
	}

	// journald's own bookkeeping does not go out. Forwarding every field means
	// forwarding whatever it adds next, without having decided to.
	if strings.Contains(out.String(), "_BOOT_ID") || strings.Contains(out.String(), "CAP_EFFECTIVE") {
		t.Errorf("output carries journald's internal fields:\n%s", out.String())
	}
}

func TestAnOddRecordDoesNotCostTheRest(t *testing.T) {
	// journald's fields vary with what produced an entry. Losing a whole log
	// because one line was strange helps nobody.
	commands := &recorder{replies: map[string]string{"journalctl": strings.Join([]string{
		"not json at all",
		`{"MESSAGE":null}`,
		journalLine,
	}, "\n")}}

	var out strings.Builder
	if err := (&Manager{Run: commands.runner()}).Logs(t.Context(), LogOptions{}, &out); err != nil {
		t.Fatalf("Logs() error = %v", err)
	}

	if lines := strings.Count(strings.TrimSpace(out.String()), "\n") + 1; lines != 1 {
		t.Errorf("wrote %d records, want just the one that parsed:\n%s", lines, out.String())
	}
}

func TestAMessageThatIsNotTextStillArrives(t *testing.T) {
	// journald sends a byte array when a message is not valid UTF-8, which a
	// kernel line with a stray byte in it will be.
	commands := &recorder{replies: map[string]string{
		"journalctl": `{"__REALTIME_TIMESTAMP":"1","PRIORITY":"3","MESSAGE":[104,105]}`,
	}}

	var out strings.Builder
	if err := (&Manager{Run: commands.runner()}).Logs(t.Context(), LogOptions{}, &out); err != nil {
		t.Fatalf("Logs() error = %v", err)
	}

	var record Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &record); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if record.Message != "hi" {
		t.Errorf("Message = %q, want the bytes decoded", record.Message)
	}
}
