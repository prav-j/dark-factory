package sessionstore

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/prav-j/dark-factory/internal/runtoken"
)

// SessionChecker implements runtoken.SessionChecker over the DDB Sessions
// table: a session is alive while status != Terminated, and token renewal is
// capped at createdAt + maxLifetime (specs/16 default 4h).
type SessionChecker struct {
	store       *Store
	maxLifetime time.Duration
	now         func() time.Time
}

var ErrSessionNotFound = errors.New("session not found")

func NewSessionChecker(store *Store, maxLifetime time.Duration) *SessionChecker {
	if maxLifetime == 0 {
		maxLifetime = 4 * time.Hour
	}
	return &SessionChecker{store: store, maxLifetime: maxLifetime, now: time.Now}
}

// SetClock overrides time (tests).
func (c *SessionChecker) SetClock(now func() time.Time) { c.now = now }

// GetSession looks a session up by id alone via the session-id-index GSI.
func (c *SessionChecker) GetSession(ctx context.Context, sessionID string) (runtoken.SessionInfo, error) {
	out, err := c.store.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                aws.String(tableSessions),
		IndexName:                aws.String("session-id-index"),
		KeyConditionExpression:   aws.String("#sid = :s"),
		ExpressionAttributeNames: map[string]string{"#sid": "sessionIdOnly"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":s": &types.AttributeValueMemberS{Value: sessionID},
		},
		Limit: aws.Int32(1),
	})
	if err != nil {
		return runtoken.SessionInfo{}, err
	}
	if len(out.Items) == 0 {
		return runtoken.SessionInfo{}, ErrSessionNotFound
	}
	sess := fromItem(out.Items[0])

	info := runtoken.SessionInfo{
		Alive:    sess.Status != "Terminated",
		Deadline: sess.CreatedAt.Add(c.maxLifetime),
	}
	if !sess.TTL.IsZero() && sess.TTL.Before(info.Deadline) {
		info.Deadline = sess.TTL
	}
	return info, nil
}
