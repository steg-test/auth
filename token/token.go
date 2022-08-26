package token

import (
	"context"
	"errors"
	"time"

	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jwt"
	"github.com/thanhpk/randstr"

	"github.com/todanni/auth/models"
)

const (
	GoogleCertsUrl             = "https://www.googleapis.com/oauth2/v3/certs"
	ToDanniTokenIssuer         = "todanni.com"
	RefreshTokenExpirationTime = time.Hour * 60 * 30
)

// ValidateGoogleToken follows the OAuth 2.0 spec to validate token
// and returns the email of the user the token belongs to
func ValidateGoogleToken(ctx context.Context, tkn string) (string, error) {
	autoRefresh := jwk.NewAutoRefresh(ctx)
	autoRefresh.Configure(GoogleCertsUrl, jwk.WithMinRefreshInterval(time.Hour*1))

	keySet, err := autoRefresh.Fetch(ctx, GoogleCertsUrl)
	if err != nil {
		return "", err
	}

	parsed, err := jwt.Parse([]byte(tkn), jwt.WithKeySet(keySet), jwt.WithValidate(true))
	if err != nil {
		return "", err
	}

	email, ok := parsed.Get("email")
	if !ok {
		return "", errors.New("couldn't find email in token")
	}

	return email.(string), nil
}

func IssueToDanniToken(email string, privateKey jwk.Key) (string, error) {
	t, err := jwt.NewBuilder().Issuer(ToDanniTokenIssuer).IssuedAt(time.Now()).Build()
	if err != nil {
		return "", err
	}

	// Set the custom claims
	t.Set("email", email)

	signedJWT, err := jwt.Sign(t, jwa.RS256, privateKey)
	if err != nil {
		return "", err
	}

	return string(signedJWT), nil
}

func IssueToDanniRefreshToken(userID int) (models.RefreshToken, error) {
	refreshToken := models.RefreshToken{
		Value:     randstr.Hex(10),
		UserID:    userID,
		Revoked:   false,
		ExpiresAt: time.Now().Add(RefreshTokenExpirationTime),
	}

	//TODO: the token should be saved in the DB

	return refreshToken, nil
}