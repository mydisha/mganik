package dto

import "time"

// MarketplaceOrderRequest adalah order yang sudah dibayar di marketplace.
// Dedupe per (channel_code, external_ref).
type MarketplaceOrderRequest struct {
	ChannelCode string                   `json:"channel_code" binding:"required,max=32"`
	ExternalRef string                   `json:"external_ref" binding:"required,max=128"`
	PlacedAt    *time.Time               `json:"placed_at"`
	Currency    string                   `json:"currency" binding:"omitempty,len=3"`
	Lines       []MarketplaceLineRequest `json:"lines" binding:"required,min=1,max=50,dive"`
}

type MarketplaceLineRequest struct {
	SKU       string `json:"sku" binding:"required,max=64"`
	Qty       int64  `json:"qty" binding:"required,gt=0,lte=1000"`
	UnitPrice int64  `json:"unit_price" binding:"gte=0,lte=1000000000000"`
	Discount  int64  `json:"discount" binding:"gte=0"`
}

// MarketplaceImportResponse: duplicate=true berarti order dengan external_ref
// ini sudah pernah diimpor (respons 200, bukan 201).
type MarketplaceImportResponse struct {
	Duplicate bool          `json:"duplicate"`
	Order     OrderResponse `json:"order"`
}
