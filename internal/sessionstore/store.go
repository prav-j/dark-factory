// Package sessionstore is the execution-plane operational store (specs/16.3):
// DynamoDB tables for sessions and runs, GSIs for the documented access
// patterns, TTL cleanup for terminated sessions, and manifest persistence.
// Postgres remains the control-plane source of truth; nothing live lives there.
package sessionstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	tableSessions = "Sessions"
	tableRuns     = "Runs"

	attrOrg     = "orgId"     // PK
	attrSession = "sessionId" // SK
	attrUser    = "userId"    // GSI user-index
	attrAgent   = "agentRef"  // GSI agent-index
	attrStatus  = "status"
	attrExpires = "expiresAt" // TTL
)

var (
	ErrNotFound = errors.New("not found")
	ErrTooLarge = errors.New("manifest exceeds DynamoDB item limit")
)

// Session is one sandbox-bound unit of work.
type Session struct {
	OrgID          string
	SessionID      string
	UserID         string
	AgentRef       string
	Status         string // Provisioning|Running|Idle|Committing|Terminated
	EnvironmentKey string
	Manifest       []byte
	CreatedAt      time.Time
	TTL            time.Time
}

// RunRecord is operational run state within a session.
type RunRecord struct {
	SessionID string
	RunID     string
	Trigger   string
	Status    string
}

// Store wraps a DynamoDB client pointed at LocalStack or AWS.
type Store struct {
	client *dynamodb.Client
}

func New(client *dynamodb.Client) *Store { return &Store{client: client} }

// EnsureTables creates Sessions/Runs with GSIs if absent, and best-effort
// enables TTL on expiresAt. Idempotent.
func (s *Store) EnsureTables(ctx context.Context) error {
	if _, err := s.client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableSessions),
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String(attrOrg), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String(attrSession), KeyType: types.KeyTypeRange},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String(attrOrg), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String(attrSession), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String(attrUser), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String(attrAgent), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sessionIdOnly"), AttributeType: types.ScalarAttributeTypeS},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{
			gsi("user-index", attrUser, attrSession),
			gsi("agent-index", attrAgent, attrSession),
			gsi("session-id-index", "sessionIdOnly", attrSession),
		},
		BillingMode: types.BillingModePayPerRequest,
	}); err != nil && !tableExists(err) {
		return fmt.Errorf("create %s: %w", tableSessions, err)
	}
	if _, err := s.client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String("SessionsLookup"),
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String(attrSession), KeyType: types.KeyTypeHash},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String(attrSession), AttributeType: types.ScalarAttributeTypeS},
		},
		BillingMode: types.BillingModePayPerRequest,
	}); err != nil && !tableExists(err) {
		return fmt.Errorf("create SessionsLookup: %w", err)
	}
	if _, err := s.client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName: aws.String(tableRuns),
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String(attrSession), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("runId"), KeyType: types.KeyTypeRange},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String(attrSession), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("runId"), AttributeType: types.ScalarAttributeTypeS},
		},
		BillingMode: types.BillingModePayPerRequest,
	}); err != nil && !tableExists(err) {
		return fmt.Errorf("create %s: %w", tableRuns, err)
	}

	_, _ = s.client.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: aws.String(tableSessions),
		TimeToLiveSpecification: &types.TimeToLiveSpecification{
			AttributeName: aws.String(attrExpires), Enabled: aws.Bool(true),
		},
	})
	return nil
}

func gsi(name, pk, sk string) types.GlobalSecondaryIndex {
	return types.GlobalSecondaryIndex{
		IndexName: aws.String(name),
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String(pk), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String(sk), KeyType: types.KeyTypeRange},
		},
		Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
	}
}

func tableExists(err error) bool {
	var re *types.ResourceInUseException
	return errors.As(err, &re)
}

func (s *Store) item(sess Session) (map[string]types.AttributeValue, error) {
	if len(sess.Manifest) > 350_000 { // stay under the 400KB item limit
		return nil, ErrTooLarge
	}
	item := map[string]types.AttributeValue{
		attrOrg:          &types.AttributeValueMemberS{Value: sess.OrgID},
		attrSession:      &types.AttributeValueMemberS{Value: sess.SessionID},
		"sessionIdOnly":  &types.AttributeValueMemberS{Value: sess.SessionID},
		attrUser:         &types.AttributeValueMemberS{Value: sess.UserID},
		attrAgent:        &types.AttributeValueMemberS{Value: sess.AgentRef},
		attrStatus:       &types.AttributeValueMemberS{Value: sess.Status},
		"environmentKey": &types.AttributeValueMemberS{Value: sess.EnvironmentKey},
		"createdAt":      &types.AttributeValueMemberN{Value: fmt.Sprint(sess.CreatedAt.Unix())},
	}
	if !sess.TTL.IsZero() {
		item[attrExpires] = &types.AttributeValueMemberN{Value: fmt.Sprint(sess.TTL.Unix())}
	}
	if len(sess.Manifest) > 0 {
		item["manifest"] = &types.AttributeValueMemberB{Value: sess.Manifest}
	}
	return item, nil
}

// PutSession creates or replaces a session record. Oversize manifests are
// rejected (ErrTooLarge) so resume-critical state is never silently dropped.
func (s *Store) PutSession(ctx context.Context, sess Session) error {
	item, err := s.item(sess)
	if err != nil {
		return err
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableSessions),
		Item:      item,
	})
	if err != nil {
		return err
	}
	// Strongly-consistent id-only lookup for token renewal paths.
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String("SessionsLookup"),
		Item: map[string]types.AttributeValue{
			attrSession: &types.AttributeValueMemberS{Value: sess.SessionID},
			attrOrg:     &types.AttributeValueMemberS{Value: sess.OrgID},
		},
	})
	return err
}

// UpdateStatus mutates only the status attribute.
func (s *Store) UpdateStatus(ctx context.Context, orgID, sessionID, status string) error {
	if orgID == "" {
		var err error
		orgID, err = s.lookupOrg(ctx, sessionID)
		if err != nil {
			return err
		}
	}
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                aws.String(tableSessions),
		Key:                      keyOf(orgID, sessionID),
		UpdateExpression:         aws.String("SET #st = :st"),
		ExpressionAttributeNames: map[string]string{"#st": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":st": &types.AttributeValueMemberS{Value: status},
		},
	})
	return err
}

// lookupOrg resolves an org id from the strongly-consistent lookup table.
func (s *Store) lookupOrg(ctx context.Context, sessionID string) (string, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String("SessionsLookup"),
		Key:            map[string]types.AttributeValue{attrSession: &types.AttributeValueMemberS{Value: sessionID}},
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return "", err
	}
	if out.Item == nil {
		return "", ErrNotFound
	}
	if v, ok := out.Item[attrOrg].(*types.AttributeValueMemberS); ok {
		return v.Value, nil
	}
	return "", ErrNotFound
}

// GetSession fetches one session by org+id.
func (s *Store) GetSession(ctx context.Context, orgID, sessionID string) (*Session, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(tableSessions),
		Key:       keyOf(orgID, sessionID),
	})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, errors.New("session not found")
	}
	return fromItem(out.Item), nil
}

func keyOf(orgID, sessionID string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		attrOrg:     &types.AttributeValueMemberS{Value: orgID},
		attrSession: &types.AttributeValueMemberS{Value: sessionID},
	}
}

func fromItem(item map[string]types.AttributeValue) *Session {
	str := func(k string) string {
		if v, ok := item[k].(*types.AttributeValueMemberS); ok {
			return v.Value
		}
		return ""
	}
	s := &Session{
		OrgID: str(attrOrg), SessionID: str(attrSession),
		UserID: str(attrUser), AgentRef: str(attrAgent),
		Status: str(attrStatus), EnvironmentKey: str("environmentKey"),
	}
	if n, ok := item["createdAt"].(*types.AttributeValueMemberN); ok {
		if sec, err := strconv.ParseInt(n.Value, 10, 64); err == nil {
			s.CreatedAt = time.Unix(sec, 0)
		}
	}
	if n, ok := item[attrExpires].(*types.AttributeValueMemberN); ok {
		if sec, err := strconv.ParseInt(n.Value, 10, 64); err == nil {
			s.TTL = time.Unix(sec, 0)
		}
	}
	if b, ok := item["manifest"].(*types.AttributeValueMemberB); ok {
		s.Manifest = b.Value
	}
	return s
}

// ListByOrg returns all sessions for an org (PK query).
func (s *Store) ListByOrg(ctx context.Context, orgID string) ([]*Session, error) {
	return s.query(ctx, tableSessions, "orgId = :o",
		map[string]types.AttributeValue{":o": &types.AttributeValueMemberS{Value: orgID}})
}

// ListResumableByUser returns non-terminated sessions for a user via GSI.
func (s *Store) ListResumableByUser(ctx context.Context, userID string) ([]*Session, error) {
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                aws.String(tableSessions),
		IndexName:                aws.String("user-index"),
		KeyConditionExpression:   aws.String("#u = :u"),
		FilterExpression:         aws.String("#st <> :term"),
		ExpressionAttributeNames: map[string]string{"#u": attrUser, "#st": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":u":    &types.AttributeValueMemberS{Value: userID},
			":term": &types.AttributeValueMemberS{Value: "Terminated"},
		},
	})
	if err != nil {
		return nil, err
	}
	var sessions []*Session
	for _, item := range out.Items {
		sessions = append(sessions, fromItem(item))
	}
	return sessions, nil
}

// CountActiveByAgent counts non-terminated sessions for an agent ref via GSI.
func (s *Store) CountActiveByAgent(ctx context.Context, agentRef string) (int, error) {
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                aws.String(tableSessions),
		IndexName:                aws.String("agent-index"),
		KeyConditionExpression:   aws.String("#a = :a"),
		FilterExpression:         aws.String("#st <> :term"),
		ExpressionAttributeNames: map[string]string{"#a": attrAgent, "#st": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":a":    &types.AttributeValueMemberS{Value: agentRef},
			":term": &types.AttributeValueMemberS{Value: "Terminated"},
		},
		Select: types.SelectCount,
	})
	if err != nil {
		return 0, err
	}
	return int(out.Count), nil
}

// PutRun records operational run state.
func (s *Store) PutRun(ctx context.Context, r RunRecord) error {
	_, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(tableRuns),
		Item: map[string]types.AttributeValue{
			attrSession: &types.AttributeValueMemberS{Value: r.SessionID},
			"runId":     &types.AttributeValueMemberS{Value: r.RunID},
			"trigger":   &types.AttributeValueMemberS{Value: r.Trigger},
			attrStatus:  &types.AttributeValueMemberS{Value: r.Status},
		},
	})
	return err
}

func (s *Store) query(ctx context.Context, table, cond string, values map[string]types.AttributeValue) ([]*Session, error) {
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:                 aws.String(table),
		KeyConditionExpression:    aws.String(cond),
		ExpressionAttributeValues: values,
	})
	if err != nil {
		return nil, err
	}
	var sessions []*Session
	for _, item := range out.Items {
		sessions = append(sessions, fromItem(item))
	}
	return sessions, nil
}
