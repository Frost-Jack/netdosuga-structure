package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/segmentio/kafka-go"
	"time"
)

type topicAdmin interface {
	CreateTopics(context.Context, *kafka.CreateTopicsRequest) (*kafka.CreateTopicsResponse, error)
}

// Readers are constructed only after both owned event streams exist. Starting a
// consumer group on a missing topic can leave it without a partition assignment.
func ensureTopics(ctx context.Context, admin topicAdmin, topics ...string) error {
	configs := make([]kafka.TopicConfig, 0, len(topics))
	for _, topic := range topics {
		configs = append(configs, kafka.TopicConfig{Topic: topic, NumPartitions: 1, ReplicationFactor: 1})
	}
	var last error
	for ctx.Err() == nil {
		response, err := admin.CreateTopics(ctx, &kafka.CreateTopicsRequest{Topics: configs})
		if err == nil {
			for _, topic := range topics {
				topicErr, exists := response.Errors[topic]
				if !exists {
					err = fmt.Errorf("missing topic creation acknowledgement")
					break
				}
				if topicErr != nil && !errors.Is(topicErr, kafka.TopicAlreadyExists) {
					err = topicErr
					break
				}
			}
		}
		if err == nil {
			return nil
		}
		last = err
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return fmt.Errorf("Kafka topic startup failed: %w (last: %v)", ctx.Err(), last)
}
