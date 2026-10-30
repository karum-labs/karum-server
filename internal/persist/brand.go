package persist

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/karum-labs/karum-server/internal/orchestrator"
)

type skuRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// BrandProfile returns the account's brand profile. ok is false when the
// account has none.
func (s *Store) BrandProfile(ctx context.Context, accountID string) (orchestrator.BrandContext, bool, error) {
	var brand orchestrator.BrandContext
	var skusJSON []byte
	err := s.pool.QueryRow(ctx, `
		SELECT name, category, description, positioning, audience, skus_json
		FROM brand_profiles
		WHERE account_id = $1
	`, accountID).Scan(&brand.Name, &brand.Category, &brand.Description, &brand.Positioning, &brand.Audience, &skusJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return orchestrator.BrandContext{}, false, nil
	}
	if err != nil {
		return orchestrator.BrandContext{}, false, err
	}
	var rows []skuRow
	if err := json.Unmarshal(skusJSON, &rows); err != nil {
		return orchestrator.BrandContext{}, false, err
	}
	for _, row := range rows {
		brand.SKUs = append(brand.SKUs, orchestrator.SKU{ID: row.ID, Name: row.Name})
	}
	return brand, true, nil
}

// PutBrandProfile creates or replaces the account's brand profile.
func (s *Store) PutBrandProfile(ctx context.Context, accountID string, brand orchestrator.BrandContext) error {
	rows := make([]skuRow, 0, len(brand.SKUs))
	for _, sku := range brand.SKUs {
		rows = append(rows, skuRow{ID: sku.ID, Name: sku.Name})
	}
	skusJSON, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO brand_profiles (account_id, name, category, description, positioning, audience, skus_json, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, now())
		ON CONFLICT (account_id) DO UPDATE SET
			name = EXCLUDED.name,
			category = EXCLUDED.category,
			description = EXCLUDED.description,
			positioning = EXCLUDED.positioning,
			audience = EXCLUDED.audience,
			skus_json = EXCLUDED.skus_json,
			updated_at = now()
	`, accountID, brand.Name, brand.Category, brand.Description, brand.Positioning, brand.Audience, skusJSON)
	return err
}
