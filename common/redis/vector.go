package redis

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
)

// DecodeFloat32Vector decodes a RediSearch FLOAT32 vector blob into []float32.
// RediSearch vector fields are typically stored as little-endian binary bytes.
func DecodeFloat32Vector(raw []byte) ([]float32, error) {
	return DecodeFloat32VectorWithOrder(raw, binary.LittleEndian)
}

// DecodeFloat32VectorWithOrder decodes a binary vector blob with the given byte order.
func DecodeFloat32VectorWithOrder(raw []byte, order binary.ByteOrder) ([]float32, error) {
	if len(raw) == 0 {
		return []float32{}, nil
	}
	if len(raw)%4 != 0 {
		return nil, fmt.Errorf("invalid FLOAT32 vector length %d: not divisible by 4", len(raw))
	}

	vector := make([]float32, len(raw)/4)
	for i := 0; i < len(vector); i++ {
		bits := order.Uint32(raw[i*4 : (i+1)*4])
		vector[i] = math.Float32frombits(bits)
	}

	return vector, nil
}

// GetHashFieldFloat32Vector fetches a binary vector field from a Redis hash and decodes it.
func GetHashFieldFloat32Vector(ctx context.Context, key, field string) ([]float32, error) {
	raw, err := Rdb.HGet(ctx, key, field).Bytes()
	if err != nil {
		return nil, fmt.Errorf("get redis hash field %s[%s]: %w", key, field, err)
	}

	vector, err := DecodeFloat32Vector(raw)
	if err != nil {
		return nil, fmt.Errorf("decode redis hash field %s[%s]: %w", key, field, err)
	}

	return vector, nil
}

// GetHashVector fetches the default RediSearch vector field named "vector" from a Redis hash.
func GetHashVector(ctx context.Context, key string) ([]float32, error) {
	return GetHashFieldFloat32Vector(ctx, key, "vector")
}
