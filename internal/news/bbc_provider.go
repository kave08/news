package news

import (
	"context"
	"errors"

	"github.com/kave08/news/internal/model"
)

var ErrBBCApprovedProviderUnavailable = errors.New("NEWS_PROVIDER=bbc_approved is reserved for an approved BBC feed/API and is intentionally disabled in this repo")

type BBCApprovedProvider struct{}

func NewBBCApprovedProvider() *BBCApprovedProvider {
	return &BBCApprovedProvider{}
}

func (p *BBCApprovedProvider) FetchLatest(context.Context, int) ([]model.NewsArticle, error) {
	return nil, ErrBBCApprovedProviderUnavailable
}
