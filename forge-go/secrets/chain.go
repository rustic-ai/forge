package secrets

import "context"

type ChainSecretProvider struct {
	providers []SecretProvider
}

func NewChainSecretProvider(providers ...SecretProvider) *ChainSecretProvider {
	return &ChainSecretProvider{
		providers: providers,
	}
}

func (p *ChainSecretProvider) Resolve(ctx context.Context, key string) (string, error) {
	for _, provider := range p.providers {
		val, err := provider.Resolve(ctx, key)
		if err == nil {
			return val, nil
		}
	}
	return "", ErrSecretNotFound
}

func DefaultProvider() SecretProvider {
	return NewChainSecretProvider(
		NewEnvSecretProvider(),
		NewDotEnvSecretProvider(""),
		NewFileSecretProvider(""),
	)
}
