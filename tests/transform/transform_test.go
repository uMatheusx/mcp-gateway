package transform_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uMatheusx/mcp-gateway/internal/transform"
)

const sampleJSON = `{
  "id": 1,
  "name": "Alice",
  "email": "alice@example.com",
  "address": {
    "city": "São Paulo",
    "country": "BR",
    "zip": "01310-100"
  },
  "phone": "11999999999"
}`

func decode(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &m))
	return m
}

func TestApply_NoFilters(t *testing.T) {
	result, err := transform.Apply([]byte(sampleJSON), nil, nil)
	require.NoError(t, err)
	m := decode(t, result)
	assert.Equal(t, float64(1), m["id"])
	assert.Equal(t, "Alice", m["name"])
}

func TestApply_IncludeTopLevel(t *testing.T) {
	result, err := transform.Apply([]byte(sampleJSON), []string{"id", "name"}, nil)
	require.NoError(t, err)
	m := decode(t, result)
	assert.Equal(t, float64(1), m["id"])
	assert.Equal(t, "Alice", m["name"])
	assert.NotContains(t, m, "email")
	assert.NotContains(t, m, "phone")
	assert.NotContains(t, m, "address")
}

func TestApply_IncludeNestedField(t *testing.T) {
	result, err := transform.Apply([]byte(sampleJSON), []string{"id", "address.city"}, nil)
	require.NoError(t, err)
	m := decode(t, result)
	assert.Equal(t, float64(1), m["id"])
	addr, ok := m["address"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "São Paulo", addr["city"])
	assert.NotContains(t, addr, "country")
	assert.NotContains(t, addr, "zip")
	assert.NotContains(t, m, "name")
}

func TestApply_ExcludeFields(t *testing.T) {
	result, err := transform.Apply([]byte(sampleJSON), nil, []string{"email", "phone"})
	require.NoError(t, err)
	m := decode(t, result)
	assert.Contains(t, m, "id")
	assert.Contains(t, m, "name")
	assert.Contains(t, m, "address")
	assert.NotContains(t, m, "email")
	assert.NotContains(t, m, "phone")
}

func TestApply_ExcludeNestedField(t *testing.T) {
	result, err := transform.Apply([]byte(sampleJSON), nil, []string{"address.zip"})
	require.NoError(t, err)
	m := decode(t, result)
	addr := m["address"].(map[string]interface{})
	assert.Contains(t, addr, "city")
	assert.Contains(t, addr, "country")
	assert.NotContains(t, addr, "zip")
}

func TestApply_ExcludeWholeNestedObject(t *testing.T) {
	result, err := transform.Apply([]byte(sampleJSON), nil, []string{"address"})
	require.NoError(t, err)
	m := decode(t, result)
	assert.NotContains(t, m, "address")
	assert.Contains(t, m, "name")
}

func TestApply_IncludeAndExcludeCombined(t *testing.T) {
	// Include address but exclude the zip within it
	result, err := transform.Apply([]byte(sampleJSON), []string{"address"}, []string{"address.zip"})
	require.NoError(t, err)
	m := decode(t, result)
	assert.NotContains(t, m, "name")
	addr := m["address"].(map[string]interface{})
	assert.Contains(t, addr, "city")
	assert.NotContains(t, addr, "zip")
}

func TestApply_NonObjectJSONPassthrough(t *testing.T) {
	data := []byte(`[{"id":1},{"id":2}]`)
	result, err := transform.Apply(data, nil, []string{"id"})
	require.NoError(t, err)
	var arr []map[string]interface{}
	require.NoError(t, json.Unmarshal(result, &arr))
	assert.NotContains(t, arr[0], "id")
}

func TestApply_InvalidJSON(t *testing.T) {
	_, err := transform.Apply([]byte("not json"), nil, []string{"field"})
	assert.Error(t, err)
}
