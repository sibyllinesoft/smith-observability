package vectorstore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/redis/go-redis/v9"
)

const (
	// defaultLimit is the default limit used for pagination and batch operations
	BatchLimit = 100
)

type RedisConfig struct {
	// Connection settings
	Addr     string `json:"addr"`               // Redis server address (host:port) - REQUIRED
	Username string `json:"username,omitempty"` // Username for Redis AUTH (optional)
	Password string `json:"password,omitempty"` // Password for Redis AUTH (optional)
	DB       int    `json:"db,omitempty"`       // Redis database number (default: 0)

	// Connection pool and timeout settings (passed directly to Redis client)
	PoolSize        int           `json:"pool_size,omitempty"`          // Maximum number of socket connections (optional)
	MaxActiveConns  int           `json:"max_active_conns,omitempty"`   // Maximum number of active connections (optional)
	MinIdleConns    int           `json:"min_idle_conns,omitempty"`     // Minimum number of idle connections (optional)
	MaxIdleConns    int           `json:"max_idle_conns,omitempty"`     // Maximum number of idle connections (optional)
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime,omitempty"`  // Connection maximum lifetime (optional)
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time,omitempty"` // Connection maximum idle time (optional)
	DialTimeout     time.Duration `json:"dial_timeout,omitempty"`       // Timeout for socket connection (optional)
	ReadTimeout     time.Duration `json:"read_timeout,omitempty"`       // Timeout for socket reads (optional)
	WriteTimeout    time.Duration `json:"write_timeout,omitempty"`      // Timeout for socket writes (optional)
	ContextTimeout  time.Duration `json:"context_timeout,omitempty"`    // Timeout for Redis operations (optional)
}

// RedisStore represents the Redis vector store.
type RedisStore struct {
	client *redis.Client
	config RedisConfig
	logger schemas.Logger
}

func (s *RedisStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]VectorStoreProperties) error {
	ctx, cancel := withTimeout(ctx, s.config.ContextTimeout)
	defer cancel()

	// Check if index already exists
	infoResult := s.client.Do(ctx, "FT.INFO", namespace)
	if infoResult.Err() == nil {
		return nil // Index already exists
	}
	if err := infoResult.Err(); err != nil && strings.Contains(strings.ToLower(err.Error()), "unknown command") {
		return fmt.Errorf("RediSearch module not available: please use Redis Stack or enable RediSearch (FT.*). Original error: %w", err)
	}

	// Extract metadata field names from properties
	var metadataFields []string
	for fieldName := range properties {
		metadataFields = append(metadataFields, fieldName)
	}

	// Create index with VECTOR field + metadata fields
	keyPrefix := fmt.Sprintf("%s:", namespace)

	if dimension <= 0 {
		return fmt.Errorf("redis vector index %q: dimension must be > 0 (got %d)", namespace, dimension)
	}

	args := []interface{}{
		"FT.CREATE", namespace,
		"ON", "HASH",
		"PREFIX", "1", keyPrefix,
		"SCHEMA",
		// Native vector field with HNSW algorithm
		"embedding", "VECTOR", "HNSW", "6",
		"TYPE", "FLOAT32",
		"DIM", dimension,
		"DISTANCE_METRIC", "COSINE",
	}

	// Add all metadata fields as TEXT with exact matching
	// All values are converted to strings for consistent searching
	for _, field := range metadataFields {
		// Detect field type from VectorStoreProperties
		prop := properties[field]
		switch prop.DataType {
		case VectorStorePropertyTypeInteger:
			args = append(args, field, "NUMERIC")
		default:
			args = append(args, field, "TAG")
		}
	}

	// Create the index
	if err := s.client.Do(ctx, args...).Err(); err != nil {
		return fmt.Errorf("failed to create semantic vector index %s: %w", namespace, err)
	}

	return nil
}

func (s *RedisStore) GetChunk(ctx context.Context, namespace string, id string) (SearchResult, error) {
	ctx, cancel := withTimeout(ctx, s.config.ContextTimeout)
	defer cancel()

	if strings.TrimSpace(id) == "" {
		return SearchResult{}, fmt.Errorf("id is required")
	}

	// Create key with namespace
	key := buildKey(namespace, id)

	// Get all fields from the hash
	result := s.client.HGetAll(ctx, key)
	if result.Err() != nil {
		return SearchResult{}, fmt.Errorf("failed to get chunk: %w", result.Err())
	}

	fields := result.Val()
	if len(fields) == 0 {
		return SearchResult{}, fmt.Errorf("chunk not found: %s", id)
	}

	// Build SearchResult
	searchResult := SearchResult{
		ID:         id,
		Properties: make(map[string]interface{}),
	}

	// Parse fields
	for k, v := range fields {
		searchResult.Properties[k] = v
	}

	return searchResult, nil
}

func (s *RedisStore) GetChunks(ctx context.Context, namespace string, ids []string) ([]SearchResult, error) {
	ctx, cancel := withTimeout(ctx, s.config.ContextTimeout)
	defer cancel()

	if len(ids) == 0 {
		return []SearchResult{}, nil
	}

	// Create keys with namespace
	keys := make([]string, len(ids))
	for i, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("id cannot be empty at index %d", i)
		}
		keys[i] = buildKey(namespace, id)
	}

	// Use pipeline for efficient batch retrieval
	pipe := s.client.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(keys))

	for i, key := range keys {
		cmds[i] = pipe.HGetAll(ctx, key)
	}

	// Execute pipeline
	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to execute pipeline: %w", err)
	}

	// Process results
	var results []SearchResult
	for i, cmd := range cmds {
		if cmd.Err() != nil {
			// Log error but continue with other results
			s.logger.Debug(fmt.Sprintf("failed to get chunk %s: %v", ids[i], cmd.Err()))
			continue
		}

		fields := cmd.Val()
		if len(fields) == 0 {
			// Chunk not found, skip
			continue
		}

		// Build SearchResult
		searchResult := SearchResult{
			ID:         ids[i],
			Properties: make(map[string]interface{}),
		}

		// Parse fields
		for k, v := range fields {
			searchResult.Properties[k] = v
		}

		results = append(results, searchResult)
	}

	return results, nil
}

func (s *RedisStore) GetAll(ctx context.Context, namespace string, queries []Query, selectFields []string, cursor *string, limit int64) ([]SearchResult, *string, error) {
	ctx, cancel := withTimeout(ctx, s.config.ContextTimeout)
	defer cancel()

	// Set default limit if not provided
	if limit < 0 {
		limit = BatchLimit
	}

	// Build Redis query from the provided queries
	redisQuery := buildRedisQuery(queries)

	// Build FT.SEARCH command
	args := []interface{}{
		"FT.SEARCH", namespace,
		redisQuery,
	}

	// Add RETURN only if specific fields were requested
	if len(selectFields) > 0 {
		args = append(args, "RETURN", len(selectFields))
		for _, field := range selectFields {
			args = append(args, field)
		}
	}

	// Add LIMIT clause - use large limit for "all" (limit=0)
	searchLimit := limit
	if limit == 0 {
		searchLimit = math.MaxInt32 // Use large limit to get all results
	}

	// Add OFFSET for pagination if cursor is provided
	offset := 0
	if cursor != nil && *cursor != "" {
		if parsedOffset, err := strconv.ParseInt(*cursor, 10, 64); err == nil {
			offset = int(parsedOffset)
		}
	}

	args = append(args, "LIMIT", offset, int(searchLimit), "DIALECT", "2")

	// Execute search
	result := s.client.Do(ctx, args...)
	if result.Err() != nil {
		return nil, nil, fmt.Errorf("failed to search: %w", result.Err())
	}

	// Parse search results
	results, err := s.parseSearchResults(result.Val(), selectFields)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse search results: %w", err)
	}

	// Implement cursor-based pagination using OFFSET
	var nextCursor *string = nil
	if cursor != nil && *cursor != "" {
		// If we have a cursor, we've already applied pagination
		// Check if there might be more results
		if len(results) == int(limit) && limit > 0 {
			// There might be more results, create next cursor
			offset, err := strconv.ParseInt(*cursor, 10, 64)
			if err == nil {
				nextOffset := offset + limit
				nextCursorStr := strconv.FormatInt(nextOffset, 10)
				nextCursor = &nextCursorStr
			}
		}
	} else if len(results) == int(limit) && limit > 0 {
		// First page and we got exactly the limit, there might be more
		nextCursorStr := strconv.FormatInt(limit, 10)
		nextCursor = &nextCursorStr
	}

	return results, nextCursor, nil
}

// parseSearchResults parses FT.SEARCH results into SearchResult slice
func (s *RedisStore) parseSearchResults(result interface{}, selectFields []string) ([]SearchResult, error) {
	// FT.SEARCH returns a map with results array
	resultMap, ok := result.(map[interface{}]interface{})
	if !ok {
		return []SearchResult{}, nil
	}

	resultsArray, ok := resultMap["results"].([]interface{})
	if !ok {
		return []SearchResult{}, nil
	}

	results := []SearchResult{}

	for _, resultItem := range resultsArray {
		resultMap, ok := resultItem.(map[interface{}]interface{})
		if !ok {
			continue
		}

		// Get the document ID
		id, ok := resultMap["id"].(string)
		if !ok {
			continue
		}

		// Extract ID from key (remove namespace prefix)
		keyParts := strings.Split(id, ":")
		if len(keyParts) < 2 {
			continue
		}
		documentID := strings.Join(keyParts[1:], ":") // Handle IDs that might contain colons

		// Get the extra_attributes (metadata)
		extraAttributes, ok := resultMap["extra_attributes"].(map[interface{}]interface{})
		if !ok {
			continue
		}

		// Build SearchResult
		searchResult := SearchResult{
			ID:         documentID,
			Properties: make(map[string]interface{}),
		}

		// Parse extra_attributes
		for fieldNameInterface, fieldValue := range extraAttributes {
			fieldName, ok := fieldNameInterface.(string)
			if !ok {
				continue
			}

			// Always include score field for vector searches
			if fieldName == "score" {
				searchResult.Properties[fieldName] = fieldValue
				// Also set the Score field for proper access
				if scoreFloat, ok := fieldValue.(float64); ok {
					searchResult.Score = &scoreFloat
				}
				continue
			}

			// Apply field selection if specified
			if len(selectFields) > 0 {
				// Check if this field should be included
				include := false
				for _, selectField := range selectFields {
					if fieldName == selectField {
						include = true
						break
					}
				}
				if !include {
					continue
				}
			}

			searchResult.Properties[fieldName] = fieldValue
		}

		results = append(results, searchResult)
	}

	return results, nil
}

// buildRedisQuery converts []Query to Redis query syntax
func buildRedisQuery(queries []Query) string {
	if len(queries) == 0 {
		return "*"
	}

	var conditions []string
	for _, query := range queries {
		condition := buildRedisQueryCondition(query)
		if condition != "" {
			conditions = append(conditions, condition)
		}
	}

	if len(conditions) == 0 {
		return "*"
	}

	// Join conditions with space (AND operation in Redis)
	return strings.Join(conditions, " ")
}

// buildRedisQueryCondition builds a single Redis query condition
func buildRedisQueryCondition(query Query) string {
	field := query.Field
	operator := query.Operator
	value := query.Value

	// Convert value to string
	var stringValue string
	switch val := value.(type) {
	case string:
		stringValue = val
	case int, int64, float64, bool:
		stringValue = fmt.Sprintf("%v", val)
	default:
		jsonData, _ := json.Marshal(val)
		stringValue = string(jsonData)
	}

	// Escape special characters for TAG fields
	escapedValue := escapeSearchValue(stringValue) // new function for TAG escaping

	switch operator {
	case QueryOperatorEqual:
		// TAG exact match
		return fmt.Sprintf("@%s:{%s}", field, escapedValue)
	case QueryOperatorNotEqual:
		// TAG negation
		return fmt.Sprintf("-@%s:{%s}", field, escapedValue)
	case QueryOperatorLike:
		// Cannot do LIKE with TAGs directly; fallback to exact match
		return fmt.Sprintf("@%s:{%s}", field, escapedValue)
	case QueryOperatorGreaterThan:
		return fmt.Sprintf("@%s:[(%s +inf]", field, escapedValue)
	case QueryOperatorGreaterThanOrEqual:
		return fmt.Sprintf("@%s:[%s +inf]", field, escapedValue)
	case QueryOperatorLessThan:
		return fmt.Sprintf("@%s:[-inf (%s]", field, escapedValue)
	case QueryOperatorLessThanOrEqual:
		return fmt.Sprintf("@%s:[-inf %s]", field, escapedValue)
	case QueryOperatorIsNull:
		// Field not present
		return fmt.Sprintf("-@%s:*", field)
	case QueryOperatorIsNotNull:
		// Field exists
		return fmt.Sprintf("@%s:*", field)
	case QueryOperatorContainsAny:
		if values, ok := value.([]interface{}); ok {
			var orConditions []string
			for _, v := range values {
				vStr := fmt.Sprintf("%v", v)
				orConditions = append(orConditions, fmt.Sprintf("@%s:{%s}", field, escapeSearchValue(vStr)))
			}
			return fmt.Sprintf("(%s)", strings.Join(orConditions, " | "))
		}
		return fmt.Sprintf("@%s:{%s}", field, escapedValue)
	case QueryOperatorContainsAll:
		if values, ok := value.([]interface{}); ok {
			var andConditions []string
			for _, v := range values {
				vStr := fmt.Sprintf("%v", v)
				andConditions = append(andConditions, fmt.Sprintf("@%s:{%s}", field, escapeSearchValue(vStr)))
			}
			return strings.Join(andConditions, " ")
		}
		return fmt.Sprintf("@%s:{%s}", field, escapedValue)
	default:
		return fmt.Sprintf("@%s:{%s}", field, escapedValue)
	}
}

func (s *RedisStore) GetNearest(ctx context.Context, namespace string, vector []float32, queries []Query, selectFields []string, threshold float64, limit int64) ([]SearchResult, error) {
	ctx, cancel := withTimeout(ctx, s.config.ContextTimeout)
	defer cancel()

	// Build Redis query from the provided queries
	redisQuery := buildRedisQuery(queries)

	// Convert query embedding to binary format
	queryBytes := float32SliceToBytes(vector)

	// Build hybrid FT.SEARCH query: metadata filters + KNN vector search
	// The correct syntax is: (metadata_filter)=>[KNN k @embedding $vec AS score]
	var hybridQuery string
	if len(queries) > 0 {
		// Wrap metadata query in parentheses for hybrid syntax
		hybridQuery = fmt.Sprintf("(%s)", redisQuery)
	} else {
		// Wildcard for pure vector search
		hybridQuery = "*"
	}

	// Execute FT.SEARCH with KNN
	// Use large limit for "all" (limit=0) in KNN query
	knnLimit := limit
	if limit == 0 {
		knnLimit = math.MaxInt32
	}

	args := []interface{}{
		"FT.SEARCH", namespace,
		fmt.Sprintf("%s=>[KNN %d @embedding $vec AS score]", hybridQuery, knnLimit),
		"PARAMS", "2", "vec", queryBytes,
		"SORTBY", "score",
	}

	// Add RETURN clause - always include score for vector search
	// For vector search, we need to include the score field generated by KNN
	returnFields := []string{"score"}
	if len(selectFields) > 0 {
		returnFields = append(returnFields, selectFields...)
	}

	args = append(args, "RETURN", len(returnFields))
	for _, field := range returnFields {
		args = append(args, field)
	}

	// Add LIMIT clause and DIALECT 2 for better query parsing
	searchLimit := limit
	if limit == 0 {
		searchLimit = math.MaxInt32
	}
	args = append(args, "LIMIT", 0, int(searchLimit), "DIALECT", "2")

	result := s.client.Do(ctx, args...)
	if result.Err() != nil {
		return nil, fmt.Errorf("native vector search failed: %w", result.Err())
	}

	// Parse search results
	results, err := s.parseSearchResults(result.Val(), selectFields)
	if err != nil {
		return nil, err
	}

	// Apply threshold filter and extract scores
	var filteredResults []SearchResult
	for _, result := range results {
		// Extract score from the result
		if scoreValue, exists := result.Properties["score"]; exists {
			var score float64
			switch v := scoreValue.(type) {
			case float64:
				score = v
			case float32:
				score = float64(v)
			case int:
				score = float64(v)
			case int64:
				score = float64(v)
			case string:
				if parsedScore, err := strconv.ParseFloat(v, 64); err == nil {
					score = parsedScore
				}
			}

			// Convert cosine distance to similarity: similarity = 1 - distance
			similarity := 1.0 - score
			result.Score = &similarity

			// Apply threshold filter
			if similarity >= threshold {
				filteredResults = append(filteredResults, result)
			}
		} else {
			// If no score, include the result (shouldn't happen with KNN queries)
			filteredResults = append(filteredResults, result)
		}
	}

	results = filteredResults

	return results, nil
}

func (s *RedisStore) Add(ctx context.Context, namespace string, id string, embedding []float32, metadata map[string]interface{}) error {
	ctx, cancel := withTimeout(ctx, s.config.ContextTimeout)
	defer cancel()

	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}

	// Create key with namespace
	key := buildKey(namespace, id)

	// Prepare hash fields: binary embedding + metadata
	fields := make(map[string]interface{})

	// Only add embedding if it's not empty
	if len(embedding) > 0 {
		// Convert float32 slice to bytes for Redis storage
		embeddingBytes := float32SliceToBytes(embedding)
		fields["embedding"] = embeddingBytes
	}

	// Add metadata fields directly (no prefix needed with proper indexing)
	for k, v := range metadata {
		switch val := v.(type) {
		case string:
			fields[k] = val
		case int, int64, float64, bool:
			fields[k] = fmt.Sprintf("%v", val)
		case []interface{}:
			// Preserve arrays as JSON to support round-trips (e.g., stream_chunks)
			b, err := json.Marshal(val)
			if err != nil {
				return fmt.Errorf("failed to marshal array metadata %s: %w", k, err)
			}
			fields[k] = string(b)
		default:
			// JSON encode complex types
			jsonData, err := json.Marshal(val)
			if err != nil {
				return fmt.Errorf("failed to marshal metadata field %s: %w", k, err)
			}
			fields[k] = string(jsonData)
		}
	}

	// Store as hash for efficient native vector search
	if err := s.client.HSet(ctx, key, fields).Err(); err != nil {
		return fmt.Errorf("failed to store semantic cache entry: %w", err)
	}

	return nil
}

func (s *RedisStore) Delete(ctx context.Context, namespace string, id string) error {
	ctx, cancel := withTimeout(ctx, s.config.ContextTimeout)
	defer cancel()

	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("id is required")
	}

	// Create key with namespace
	key := buildKey(namespace, id)

	// Delete the hash key
	result := s.client.Del(ctx, key)
	if result.Err() != nil {
		return fmt.Errorf("failed to delete chunk %s: %w", id, result.Err())
	}

	// Check if the key actually existed
	if result.Val() == 0 {
		return fmt.Errorf("chunk not found: %s", id)
	}

	return nil
}

func (s *RedisStore) DeleteAll(ctx context.Context, namespace string, queries []Query) ([]DeleteResult, error) {
	ctx, cancel := withTimeout(ctx, s.config.ContextTimeout)
	defer cancel()

	// Use cursor-based deletion to handle large datasets efficiently
	return s.deleteAllWithCursor(ctx, namespace, queries, nil)
}

// deleteAllWithCursor performs cursor-based deletion for large datasets
func (s *RedisStore) deleteAllWithCursor(ctx context.Context, namespace string, queries []Query, cursor *string) ([]DeleteResult, error) {
	// Get a batch of documents to delete (using pagination)
	results, nextCursor, err := s.GetAll(ctx, namespace, queries, []string{}, cursor, BatchLimit)
	if err != nil {
		return nil, fmt.Errorf("failed to find documents to delete: %w", err)
	}

	if len(results) == 0 {
		return []DeleteResult{}, nil
	}

	// Extract IDs from results
	ids := make([]string, len(results))
	for i, result := range results {
		ids[i] = result.ID
	}

	// Delete this batch of documents
	var deleteResults []DeleteResult
	batchSize := BatchLimit // Process in batches to avoid overwhelming Redis

	for i := 0; i < len(ids); i += batchSize {
		end := i + batchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]

		// Create pipeline for batch deletion
		pipe := s.client.Pipeline()
		cmds := make([]*redis.IntCmd, len(batch))

		for j, id := range batch {
			key := buildKey(namespace, id)
			cmds[j] = pipe.Del(ctx, key)
		}

		// Execute pipeline
		_, err := pipe.Exec(ctx)
		if err != nil {
			// If pipeline fails, mark all in this batch as failed
			for _, id := range batch {
				deleteResults = append(deleteResults, DeleteResult{
					ID:     id,
					Status: DeleteStatusError,
					Error:  fmt.Sprintf("pipeline execution failed: %v", err),
				})
			}
			continue
		}

		// Process results for this batch
		for j, cmd := range cmds {
			id := batch[j]
			if cmd.Err() != nil {
				deleteResults = append(deleteResults, DeleteResult{
					ID:     id,
					Status: DeleteStatusError,
					Error:  cmd.Err().Error(),
				})
			} else if cmd.Val() > 0 {
				// Key existed and was deleted
				deleteResults = append(deleteResults, DeleteResult{
					ID:     id,
					Status: DeleteStatusSuccess,
				})
			} else {
				// Key didn't exist
				deleteResults = append(deleteResults, DeleteResult{
					ID:     id,
					Status: DeleteStatusError,
					Error:  "document not found",
				})
			}
		}
	}

	// If there are more results, continue with next cursor
	if nextCursor != nil {
		nextResults, err := s.deleteAllWithCursor(ctx, namespace, queries, nextCursor)
		if err != nil {
			return nil, fmt.Errorf("failed to delete remaining documents: %w", err)
		}
		// Combine results from this batch and subsequent batches
		deleteResults = append(deleteResults, nextResults...)
	}

	return deleteResults, nil
}

func (s *RedisStore) DeleteNamespace(ctx context.Context, namespace string) error {
	ctx, cancel := withTimeout(ctx, s.config.ContextTimeout)
	defer cancel()

	// Drop the index using FT.DROPINDEX
	if err := s.client.Do(ctx, "FT.DROPINDEX", namespace).Err(); err != nil {
		// Check if error is "Unknown Index name" - that's OK, index doesn't exist
		if strings.Contains(err.Error(), "Unknown Index name") {
			return nil // Index doesn't exist, nothing to drop
		}
		return fmt.Errorf("failed to drop semantic index %s: %w", namespace, err)
	}

	return nil
}

func (s *RedisStore) Close(ctx context.Context, namespace string) error {
	// Close the Redis client connection
	return s.client.Close()
}

// escapeSearchValue escapes special characters in search values.
func escapeSearchValue(value string) string {
	// Escape special RediSearch characters
	replacer := strings.NewReplacer(
		"(", "\\(",
		")", "\\)",
		"[", "\\[",
		"]", "\\]",
		"{", "\\{",
		"}", "\\}",
		"*", "\\*",
		"?", "\\?",
		"|", "\\|",
		"&", "\\&",
		"!", "\\!",
		"@", "\\@",
		"#", "\\#",
		"$", "\\$",
		"%", "\\%",
		"^", "\\^",
		"~", "\\~",
		"`", "\\`",
		"\"", "\\\"",
		"'", "\\'",
		" ", "\\ ",
		"-", "\\-",
		",", "|",
	)
	return replacer.Replace(value)
}

// Binary embedding conversion helpers
func float32SliceToBytes(floats []float32) []byte {
	bytes := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(bytes[i*4:], math.Float32bits(f))
	}
	return bytes
}

// buildKey creates a Redis key by combining namespace and id.
func buildKey(namespace, id string) string {
	return fmt.Sprintf("%s:%s", namespace, id)
}

// newRedisStore creates a new Redis vector store.
func newRedisStore(ctx context.Context, config RedisConfig, logger schemas.Logger) (*RedisStore, error) {
	// Validate required fields
	if config.Addr == "" {
		return nil, fmt.Errorf("redis addr is required")
	}

	client := redis.NewClient(&redis.Options{
		Addr:            config.Addr,
		Username:        config.Username,
		Password:        config.Password,
		DB:              config.DB,
		Protocol:        3, // Explicitly use RESP3 protocol
		PoolSize:        config.PoolSize,
		MaxActiveConns:  config.MaxActiveConns,
		MinIdleConns:    config.MinIdleConns,
		MaxIdleConns:    config.MaxIdleConns,
		ConnMaxLifetime: config.ConnMaxLifetime,
		ConnMaxIdleTime: config.ConnMaxIdleTime,
		DialTimeout:     config.DialTimeout,
		ReadTimeout:     config.ReadTimeout,
		WriteTimeout:    config.WriteTimeout,
	})

	store := &RedisStore{
		client: client,
		config: config,
		logger: logger,
	}

	return store, nil
}
