package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	inputModerationQueueStream = "input_moderation:jobs"
	inputModerationQueueGroup  = "input_moderation_workers"
	inputModerationPayloadKey  = "ciphertext"
)

type inputModerationTaskQueue struct {
	rdb *redis.Client
}

func NewInputModerationTaskQueue(rdb *redis.Client) service.InputModerationTaskQueue {
	return &inputModerationTaskQueue{rdb: rdb}
}

func (q *inputModerationTaskQueue) EnsureConsumerGroup(ctx context.Context) error {
	if q == nil || q.rdb == nil {
		return fmt.Errorf("input moderation redis queue is unavailable")
	}
	err := q.rdb.XGroupCreateMkStream(ctx, inputModerationQueueStream, inputModerationQueueGroup, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func (q *inputModerationTaskQueue) Enqueue(ctx context.Context, ciphertext string) error {
	if q == nil || q.rdb == nil || strings.TrimSpace(ciphertext) == "" {
		return fmt.Errorf("input moderation queue payload is empty")
	}
	return q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: inputModerationQueueStream,
		Values: map[string]any{inputModerationPayloadKey: ciphertext},
	}).Err()
}

func (q *inputModerationTaskQueue) Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]service.InputModerationQueueMessage, error) {
	if q == nil || q.rdb == nil {
		return nil, fmt.Errorf("input moderation redis queue is unavailable")
	}
	streams, err := q.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    inputModerationQueueGroup,
		Consumer: consumer,
		Streams:  []string{inputModerationQueueStream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return inputModerationQueueMessages(streams), nil
}

func (q *inputModerationTaskQueue) ClaimStale(ctx context.Context, consumer string, minIdle time.Duration, count int64) ([]service.InputModerationQueueMessage, error) {
	if q == nil || q.rdb == nil {
		return nil, fmt.Errorf("input moderation redis queue is unavailable")
	}
	messages, _, err := q.rdb.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   inputModerationQueueStream,
		Group:    inputModerationQueueGroup,
		Consumer: consumer,
		MinIdle:  minIdle,
		Start:    "0-0",
		Count:    count,
	}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stream := redis.XStream{Stream: inputModerationQueueStream, Messages: messages}
	return inputModerationQueueMessages([]redis.XStream{stream}), nil
}

func (q *inputModerationTaskQueue) Ack(ctx context.Context, messageID string) error {
	if q == nil || q.rdb == nil || strings.TrimSpace(messageID) == "" {
		return nil
	}
	pipe := q.rdb.TxPipeline()
	pipe.XAck(ctx, inputModerationQueueStream, inputModerationQueueGroup, messageID)
	pipe.XDel(ctx, inputModerationQueueStream, messageID)
	_, err := pipe.Exec(ctx)
	return err
}

func inputModerationQueueMessages(streams []redis.XStream) []service.InputModerationQueueMessage {
	out := make([]service.InputModerationQueueMessage, 0)
	for _, stream := range streams {
		for _, message := range stream.Messages {
			value := message.Values[inputModerationPayloadKey]
			ciphertext := ""
			switch typed := value.(type) {
			case string:
				ciphertext = typed
			case []byte:
				ciphertext = string(typed)
			default:
				continue
			}
			if strings.TrimSpace(ciphertext) == "" {
				continue
			}
			out = append(out, service.InputModerationQueueMessage{ID: message.ID, Ciphertext: ciphertext})
		}
	}
	return out
}
