package cctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Corium-OS/Corium/internal/etcd"
)

// ErrEtcdNotWhole reports an etcd cluster with a member that is unhealthy or
// unaccounted for, which taking a controller out could cost its quorum.
var ErrEtcdNotWhole = errors.New("etcd is not healthy on every member")

// DefaultEtcdSettle is how long a controller's etcd member gets to catch up
// after it was moved.
const DefaultEtcdSettle = 5 * time.Minute

// etcdSource is one controller to ask about its own member.
type etcdSource struct {
	address string
	connect func() (*Client, error)
}

// etcdAnswer is one controller's reply.
type etcdAnswer struct {
	address string
	status  *etcd.Status
	err     error
}

// etcdVerdict is what the answers add up to.
type etcdVerdict struct {
	healthy, members int
	notes            []string
}

// etcdGate checks etcd around each controller a rollout moves.
//
// No node can answer for the cluster: each reports its own member, and the
// verdict comes from putting every answer side by side here, where the list of
// controllers exists.
type etcdGate struct {
	sources []etcdSource
	settle  time.Duration
	poll    time.Duration
	say     func(format string, args ...any)
	said    map[string]bool
}

// check asks once. A nil gate passes.
func (g *etcdGate) check(ctx context.Context) error {
	if g == nil || len(g.sources) == 0 {
		return nil
	}

	verdict, err := judgeEtcd(g.ask(ctx))
	g.report(verdict, err)

	return err
}

// wait asks until etcd is whole or settle runs out.
func (g *etcdGate) wait(ctx context.Context) error {
	if g == nil || len(g.sources) == 0 {
		return nil
	}

	deadline := time.Now().Add(g.settle)

	for {
		verdict, err := judgeEtcd(g.ask(ctx))
		if err == nil || time.Now().After(deadline) {
			g.report(verdict, err)

			if err != nil {
				return fmt.Errorf("after %s: %w", g.settle, err)
			}

			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(g.poll):
		}
	}
}

func (g *etcdGate) ask(ctx context.Context) []etcdAnswer {
	answers := make([]etcdAnswer, 0, len(g.sources))

	for _, source := range g.sources {
		answer := etcdAnswer{address: source.address}

		client, err := source.connect()
		if err == nil {
			answer.status, err = client.Etcd(ctx)
		}

		answer.err = err
		answers = append(answers, answer)
	}

	return answers
}

func (g *etcdGate) report(verdict etcdVerdict, err error) {
	for _, note := range verdict.notes {
		if g.said[note] {
			continue
		}

		if g.said == nil {
			g.said = map[string]bool{}
		}

		g.said[note] = true
		g.say("        etcd: %s\n", note)
	}

	if err == nil && verdict.members > 0 {
		g.say("        etcd: %d of %d members healthy\n", verdict.healthy, verdict.members)
	}
}

// judgeEtcd decides whether every etcd member is healthy and vouched for by a
// controller that was asked.
//
// A member nobody vouches for blocks: a dead machine still listed counts
// against the quorum exactly as a live unhealthy one does. The one exception is
// a controller too old to answer, whose member cannot be told from a stranger.
func judgeEtcd(answers []etcdAnswer) (etcdVerdict, error) {
	var (
		verdict    etcdVerdict
		problems   []string
		membership []string
		seenBy     string
		unchecked  bool
	)

	claimed := map[string]string{}

	for _, answer := range answers {
		switch {
		case errors.Is(answer.err, ErrUnknownRoute):
			verdict.notes = append(verdict.notes,
				answer.address+" predates the etcd check; its member is not checked")
			unchecked = true

			continue
		case answer.err != nil:
			problems = append(problems, fmt.Sprintf("%s: %v", answer.address, answer.err))

			continue
		case answer.status.Storage != etcd.StorageEtcd:
			verdict.notes = append(verdict.notes, fmt.Sprintf(
				"%s uses %s storage; no etcd member to check", answer.address, answer.status.Storage))

			continue
		}

		member := answer.status.Member

		if other, ok := claimed[member]; ok {
			problems = append(problems, fmt.Sprintf(
				"%s and %s both report member %q", other, answer.address, member))
		}

		claimed[member] = answer.address

		if !answer.status.Healthy {
			problems = append(problems, fmt.Sprintf(
				"%s: member %q is unhealthy: %s", answer.address, member, answer.status.Reason))

			continue
		}

		names := memberNames(answer.status.Members)

		if !slices.Contains(names, member) {
			problems = append(problems, fmt.Sprintf(
				"%s reports member %q, which etcd does not list", answer.address, member))
		}

		if membership == nil {
			membership, seenBy = names, answer.address
		} else if !slices.Equal(membership, names) {
			problems = append(problems, fmt.Sprintf("%s sees members [%s], %s sees [%s]",
				seenBy, strings.Join(membership, " "), answer.address, strings.Join(names, " ")))
		}
	}

	for _, name := range membership {
		if _, ok := claimed[name]; ok {
			verdict.healthy++

			continue
		}

		if unchecked {
			verdict.notes = append(verdict.notes, fmt.Sprintf("member %q is not checked", name))

			continue
		}

		problems = append(problems, fmt.Sprintf(
			"member %q is on none of the controllers given; list every controller, "+
				"or remove a member that is gone with `k0s etcd leave`", name))
	}

	if len(problems) > 0 {
		return verdict, fmt.Errorf("%w: %s", ErrEtcdNotWhole, strings.Join(problems, "; "))
	}

	verdict.members = len(membership)

	return verdict, nil
}

func memberNames(members []etcd.Member) []string {
	names := make([]string, 0, len(members))
	for _, member := range members {
		names = append(names, member.Name)
	}

	slices.Sort(names)

	return names
}
