package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/karum-labs/karum-server/internal/auth"
	"github.com/karum-labs/karum-server/internal/orchestrator"
)

// BrandStore keeps one brand profile per account. internal/persist
// implements it on Postgres; memBrandStore is the fallback when no database
// is attached.
type BrandStore interface {
	BrandProfile(ctx context.Context, accountID string) (orchestrator.BrandContext, bool, error)
	PutBrandProfile(ctx context.Context, accountID string, brand orchestrator.BrandContext) error
}

type memBrandStore struct {
	mu     sync.RWMutex
	brands map[string]orchestrator.BrandContext
}

func newMemBrandStore() *memBrandStore {
	return &memBrandStore{brands: map[string]orchestrator.BrandContext{}}
}

func (m *memBrandStore) BrandProfile(_ context.Context, accountID string) (orchestrator.BrandContext, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	brand, ok := m.brands[accountID]
	return brand, ok, nil
}

func (m *memBrandStore) PutBrandProfile(_ context.Context, accountID string, brand orchestrator.BrandContext) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.brands[accountID] = brand
	return nil
}

// Field caps keep a profile short enough to sit inside the pitch prompt.
const (
	brandNameMax    = 120
	brandFieldMax   = 600
	brandSKUMax     = 20
	brandSKUNameMax = 80
)

// brandProfileBody is the account brand profile on the wire.
type brandProfileBody struct {
	Name        string    `json:"name"`
	Category    string    `json:"category"`
	Description string    `json:"description"`
	Positioning string    `json:"positioning"`
	Audience    string    `json:"audience"`
	SKUs        []skuBody `json:"skus"`
}

func brandBodyOf(brand orchestrator.BrandContext) brandProfileBody {
	body := brandProfileBody{
		Name: brand.Name, Category: brand.Category, Description: brand.Description,
		Positioning: brand.Positioning, Audience: brand.Audience, SKUs: []skuBody{},
	}
	for _, sku := range brand.SKUs {
		body.SKUs = append(body.SKUs, skuBody{ID: sku.ID, Name: sku.Name})
	}
	return body
}

func (b brandProfileBody) toBrand() (orchestrator.BrandContext, error) {
	brand := orchestrator.BrandContext{
		Name:        strings.TrimSpace(b.Name),
		Category:    strings.TrimSpace(b.Category),
		Description: strings.TrimSpace(b.Description),
		Positioning: strings.TrimSpace(b.Positioning),
		Audience:    strings.TrimSpace(b.Audience),
	}
	if brand.Name == "" {
		return brand, fmt.Errorf("the brand name is required")
	}
	if len(brand.Name) > brandNameMax {
		return brand, fmt.Errorf("the brand name is longer than %d characters", brandNameMax)
	}
	for label, value := range map[string]string{
		"category": brand.Category, "description": brand.Description,
		"positioning": brand.Positioning, "audience": brand.Audience,
	} {
		if len(value) > brandFieldMax {
			return brand, fmt.Errorf("the %s is longer than %d characters", label, brandFieldMax)
		}
	}
	if len(b.SKUs) > brandSKUMax {
		return brand, fmt.Errorf("a brand profile holds at most %d products", brandSKUMax)
	}
	for i, sku := range b.SKUs {
		name := strings.TrimSpace(sku.Name)
		if name == "" {
			continue
		}
		if len(name) > brandSKUNameMax {
			return brand, fmt.Errorf("a product name is longer than %d characters", brandSKUNameMax)
		}
		id := strings.TrimSpace(sku.ID)
		if id == "" {
			id = fmt.Sprintf("sku-%d", i+1)
		}
		brand.SKUs = append(brand.SKUs, orchestrator.SKU{ID: id, Name: name})
	}
	return brand, nil
}

// getAccountBrand returns the signed-in account's brand profile, or an empty
// one when none is saved yet.
func (h *Handler) getAccountBrand(c *gin.Context) {
	account, ok := auth.Current(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthorized", "sign in is required")
		return
	}
	brand, found, err := h.brands.BrandProfile(c.Request.Context(), account.ID)
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, "upstream", "the brand profile could not be read")
		return
	}
	c.JSON(http.StatusOK, gin.H{"brand": brandBodyOf(brand), "saved": found})
}

// putAccountBrand saves the signed-in account's brand profile.
func (h *Handler) putAccountBrand(c *gin.Context) {
	account, ok := auth.Current(c)
	if !ok {
		writeError(c, http.StatusUnauthorized, "unauthorized", "sign in is required")
		return
	}
	var body brandProfileBody
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "validation", "the brand profile is not valid JSON")
		return
	}
	brand, err := body.toBrand()
	if err != nil {
		writeError(c, http.StatusBadRequest, "validation", err.Error())
		return
	}
	if err := h.brands.PutBrandProfile(c.Request.Context(), account.ID, brand); err != nil {
		writeError(c, http.StatusServiceUnavailable, "upstream", "the brand profile could not be saved")
		return
	}
	c.JSON(http.StatusOK, gin.H{"brand": brandBodyOf(brand), "saved": true})
}

// pitchBrand is the signed-in caller's brand profile, or nil when the caller
// is anonymous or has not saved one; the run's own brand is then used.
func (h *Handler) pitchBrand(c *gin.Context) *orchestrator.BrandContext {
	account, ok := auth.Current(c)
	if !ok {
		return nil
	}
	brand, found, err := h.brands.BrandProfile(c.Request.Context(), account.ID)
	if err != nil || !found {
		return nil
	}
	return &brand
}
