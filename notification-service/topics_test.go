package main

import (
	"context"
	"errors"
	"github.com/segmentio/kafka-go"
	"testing"
	"time"
)

type fakeTopicAdmin struct {
	calls   int
	failure error
}

func (f *fakeTopicAdmin) CreateTopics(_ context.Context, r *kafka.CreateTopicsRequest) (*kafka.CreateTopicsResponse, error) {
	f.calls++
	out := &kafka.CreateTopicsResponse{Errors: map[string]error{}}
	for _, topic := range r.Topics {
		if topic.NumPartitions != 1 || topic.ReplicationFactor != 1 {
			panic("invalid local topic configuration")
		}
		out.Errors[topic.Topic] = f.failure
	}
	return out, nil
}
func TestEnsureBothTopicsBeforeConsumerStartup(t *testing.T) {
	for _, failure := range []error{nil, kafka.TopicAlreadyExists} {
		admin := &fakeTopicAdmin{failure: failure}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := ensureTopics(ctx, admin, "legacy", "business")
		cancel()
		if err != nil || admin.calls != 1 {
			t.Fatal("fresh/existing topics must both be acknowledged before readers start", err, admin.calls)
		}
	}
}
func TestTopicStartupFailsWithinBound(t *testing.T) {
	admin := &fakeTopicAdmin{failure: kafka.NotController}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := ensureTopics(ctx, admin, "legacy", "business"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unavailable topic must prevent consumer startup", err)
	}
}
