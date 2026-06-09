package domain

import "context"

type Category struct {
	Code      string `json:"code"`
	Label     string `json:"label"`
	SortOrder int    `json:"sort_order"`
	Active    bool   `json:"active"`
}

type CategoryRepository interface {
	ListActive(ctx context.Context) ([]Category, error)
}
