package systemd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Log reading bounds.
//
// A journal is unbounded and an API is not. These are not guesses at what
// somebody wants: they are what stops one request from spending a node's
// memory or holding a connection open for a week.
const (
	// DefaultLines is a screenful and a bit -- enough to see what happened
	// without paging.
	DefaultLines = 200

	// MaxLines caps a single request. Anybody who needs more than this is
	// reading the journal wrong way round and should narrow with Since.
	MaxLines = 10000

	// MaxFollow bounds how long a stream stays open. A follow that never ends
	// is a file descriptor nobody remembers opening.
	MaxFollow = time.Hour

	// maxLineLength caps one journal record. Journald will happily carry a
	// megabyte of someone's stack trace, and re-emitting it unbounded turns a
	// log read into a memory problem.
	maxLineLength = 256 << 10
)

// LogOptions is one request for a journal.
type LogOptions struct {
	// Unit is a name from the allowlist, or KernelUnit. Empty means every unit
	// on the list, which is what an operator wants when they do not yet know
	// where the problem is.
	Unit string

	// Lines is how many records to start from.
	Lines int

	// Since is a duration before now, such as "15m". Empty means no bound
	// beyond Lines.
	Since string

	// Follow keeps the stream open as new records arrive.
	Follow bool
}

// Record is one journal entry, reduced to what is worth sending.
//
// journald attaches dozens of fields to every record -- boot IDs, cgroup
// paths, the invocation ID, the sender's SELinux context. None of it helps
// somebody reading a log, and forwarding all of it means forwarding whatever
// journald adds next without having decided to.
type Record struct {
	Time     time.Time `json:"time"`
	Unit     string    `json:"unit,omitempty"`
	Priority int       `json:"priority"`
	Message  string    `json:"message"`
}

// journalEntry is the subset of journald's JSON that is read.
type journalEntry struct {
	Timestamp string `json:"__REALTIME_TIMESTAMP"`
	Unit      string `json:"_SYSTEMD_UNIT"`
	Priority  string `json:"PRIORITY"`
	Message   any    `json:"MESSAGE"`
}

// CheckLogOptions reports whether a request is one this package will serve.
//
// It exists so a caller can find out before committing to a response. Once a
// stream has started there is no way to change its status code, so an HTTP
// handler must ask this first or an unknown unit becomes a 200 with nothing in
// it instead of a refusal.
func (m *Manager) CheckLogOptions(options LogOptions) error {
	_, err := m.journalArguments(options)

	return err
}

// Logs streams a journal, one JSON record per line, to w.
//
// Newline-delimited JSON rather than one array, because the point of Follow is
// that a client can act on a record before the request ends -- and an array
// cannot be read until it closes. Each record is flushed as it is written.
//
// The caller is responsible for the write deadline: an http.Server's
// WriteTimeout will cut a followed stream off mid-sentence, which is a bug
// that only appears once somebody watches a quiet log for longer than the
// timeout.
func (m *Manager) Logs(ctx context.Context, options LogOptions, w io.Writer) error {
	arguments, err := m.journalArguments(options)
	if err != nil {
		return err
	}

	if options.Follow {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeout(ctx, MaxFollow)
		defer cancel()
	}

	if m.Run != nil {
		// Under test: the runner returns the whole output at once, so there is
		// nothing to stream and nothing to follow.
		output, err := m.Run(ctx, "journalctl", arguments...)
		if err != nil {
			return err
		}

		return copyRecords(ctx, strings.NewReader(string(output)), w)
	}

	command := exec.CommandContext(ctx, "journalctl", arguments...)

	// Kill the whole process rather than waiting for it to notice: journalctl
	// --follow does not return on its own, and a request that has gone away
	// should not leave one running.
	command.Cancel = func() error { return command.Process.Kill() }

	stdout, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("reading from journalctl: %w", err)
	}

	if err := command.Start(); err != nil {
		return fmt.Errorf("starting journalctl: %w", err)
	}

	copyErr := copyRecords(ctx, stdout, w)

	// A cancelled follow kills journalctl, so a non-zero exit after the
	// context is done is this function's own doing rather than a failure.
	if err := command.Wait(); err != nil && ctx.Err() == nil && copyErr == nil {
		return fmt.Errorf("journalctl: %w", err)
	}

	return copyErr
}

// journalArguments turns a request into a command line, refusing anything the
// allowlist does not cover.
//
// This is the only place a client's input reaches a command, and every value
// that does is either checked against the allowlist or produced by strconv --
// no string a caller sent is passed through.
func (m *Manager) journalArguments(options LogOptions) ([]string, error) {
	arguments := []string{"--output=json", "--no-pager"}

	switch options.Unit {
	case KernelUnit:
		arguments = append(arguments, "--dmesg")

	case "":
		// Everything Corium knows about, rather than the whole journal: the
		// allowlist is about what this API will name, and "no unit" must not
		// be a way around it.
		for _, unit := range known {
			arguments = append(arguments, "--unit", unit.Name)
		}

	default:
		unit, ok := lookup(options.Unit)
		if !ok {
			return nil, fmt.Errorf("%q: %w", options.Unit, ErrUnknownUnit)
		}

		arguments = append(arguments, "--unit", unit.Name)
	}

	lines := options.Lines
	switch {
	case lines <= 0:
		lines = DefaultLines
	case lines > MaxLines:
		lines = MaxLines
	}

	arguments = append(arguments, "--lines", strconv.Itoa(lines))

	if options.Since != "" {
		since, err := time.ParseDuration(options.Since)
		if err != nil {
			return nil, fmt.Errorf("since: %q is not a duration; use a form like 15m", options.Since)
		}

		if since <= 0 {
			return nil, fmt.Errorf("since: %q is not in the past", options.Since)
		}

		// Rendered as an absolute time rather than passed through, so that
		// nothing a caller typed becomes part of the command line.
		arguments = append(arguments, "--since",
			time.Now().Add(-since).Format("2006-01-02 15:04:05"))
	}

	if options.Follow {
		arguments = append(arguments, "--follow")
	}

	return arguments, nil
}

// copyRecords reads journald's JSON and writes this package's, flushing as it
// goes so that a follower sees each line as it happens.
func copyRecords(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), maxLineLength)

	encoder := json.NewEncoder(w)
	flusher, canFlush := w.(interface{ Flush() error })

	for scanner.Scan() {
		// A cancelled request is how a followed stream normally ends: the
		// operator pressed ^C. Reporting the context error would turn every
		// ordinary `cctl logs -f` into a logged failure.
		if ctx.Err() != nil {
			return nil //nolint:nilerr // cancellation is the expected ending here
		}

		record, ok := convert(scanner.Bytes())
		if !ok {
			// A record this does not understand is skipped rather than fatal.
			// journald's fields vary with what produced the entry, and losing
			// the rest of a log because one line was odd helps nobody.
			continue
		}

		if err := encoder.Encode(record); err != nil {
			// The client went away mid-stream, which is the ordinary end of a
			// followed request rather than a failure of this node. There is
			// also nowhere left to report it: the connection is the thing that
			// broke.
			return nil //nolint:nilerr // a disconnected client is not this node's error
		}

		if canFlush {
			_ = flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		return fmt.Errorf("reading the journal: %w", err)
	}

	return nil
}

func convert(line []byte) (Record, bool) {
	var entry journalEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return Record{}, false
	}

	message, ok := decodeMessage(entry.Message)
	if !ok {
		return Record{}, false
	}

	record := Record{
		Unit:     entry.Unit,
		Message:  message,
		Priority: atoiOr(entry.Priority, 6),
	}

	// journald's timestamps are microseconds since the epoch, as a string.
	if micros, err := strconv.ParseInt(entry.Timestamp, 10, 64); err == nil {
		record.Time = time.UnixMicro(micros).UTC()
	}

	return record, true
}

// decodeMessage copes with journald sending a message as an array of byte
// values rather than a string, which is what it does when the message is not
// valid UTF-8 -- a kernel message with a stray byte in it, most often.
func decodeMessage(message any) (string, bool) {
	switch value := message.(type) {
	case string:
		return value, true

	case []any:
		bytes := make([]byte, 0, len(value))

		for _, item := range value {
			number, ok := item.(float64)
			if !ok {
				return "", false
			}

			bytes = append(bytes, byte(number))
		}

		return string(bytes), true

	default:
		return "", false
	}
}

func atoiOr(value string, fallback int) int {
	number, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}

	return number
}
