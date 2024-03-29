package auth

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/openshift/osin"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"

	"github.com/todanni/auth/config"
	"github.com/todanni/auth/models"
	"github.com/todanni/auth/storage"
	"github.com/todanni/auth/token"
)

type AuthService interface {
	LoginHandler(w http.ResponseWriter, r *http.Request)
	CallbackHandler(w http.ResponseWriter, r *http.Request)
	RefreshTokenHandler(w http.ResponseWriter, r *http.Request)
	UserInfoHandler(w http.ResponseWriter, r *http.Request)
}

type authService struct {
	router           *mux.Router
	server           *osin.Server
	userStorage      *storage.UserStorage
	dashboardStorage storage.DashboardStorage
	projectStorage   storage.ProjectStorage
	config           config.Config
	oauthConfig      *oauth2.Config
}

const (
	AccessTokenCookieName  = "todanni-access-token"
	RefreshTokenCookieName = "todanni-refresh-token"
)

func NewAuthService(router *mux.Router, conf config.Config, userStorage *storage.UserStorage, dashboardStorage storage.DashboardStorage, projectStorage storage.ProjectStorage, oauthConfig *oauth2.Config) AuthService {
	server := &authService{
		oauthConfig:      oauthConfig,
		config:           conf,
		router:           router,
		userStorage:      userStorage,
		dashboardStorage: dashboardStorage,
		projectStorage:   projectStorage,
	}
	server.routes()
	return server
}

func (s *authService) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	log.Info("Received callback request")
	ctx := context.Background()

	code := r.URL.Query().Get("code")
	log.Info(s.oauthConfig)
	log.Info(s.oauthConfig.RedirectURL)

	tok, err := s.oauthConfig.Exchange(ctx, code)
	if err != nil {
		log.Errorf("Couldn't exchange token: %v", err)
		http.Error(w, "couldn't exchange token for code", http.StatusInternalServerError)
		return
	}

	idToken := tok.Extra("id_token").(string)
	log.Info(idToken)

	email, err := token.ValidateGoogleToken(ctx, idToken)
	if err != nil {
		log.Errorf("Couldn't validate token: %v", err)
		http.Error(w, "invalid Google token", http.StatusBadRequest)
		return
	}

	// Check if user exists
	result, err := s.userStorage.GetUser(email)
	if err != nil {
		log.Errorf("Couldn't check if user exists: %v", err)
		http.Error(w, "some error with user", http.StatusInternalServerError)
		return
	}

	// User doesn't exist, we have to create it
	if result.ID == 0 {
		result, err = s.userStorage.CreateUser(email, "google", "https://www.dictionary.com/e/wp-content/uploads/2018/03/rickrolling-300x300.jpg")
		if err != nil {
			log.Errorf("Couldn't create user: %v", err)
			http.Error(w, "couldn't create new user", http.StatusInternalServerError)
			return
		}
	}

	dashboards := make([]models.Dashboard, 0)
	projects := make([]models.Project, 0)

	if result.ID != 0 {
		dashboards, err = s.dashboardStorage.List(result.ID)
		if err != nil {
			log.Error("couldn't look up user dashboards")
		}

		projects, err = s.projectStorage.List(result.ID)
		if err != nil {
			log.Error("couldn't look up user dashboards")
		}
	}

	accessToken, err := token.IssueToDanniToken(result, s.config.PrivateJWK, dashboards, projects)
	if err != nil {
		log.Errorf("Couldn't issue todanni token: %v", err)
		http.Error(w, "couldn't create the ToDanni token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// Set access and refresh token cookies
	http.SetCookie(w, &http.Cookie{
		Name:     AccessTokenCookieName,
		Value:    accessToken,
		Path:     "/",
		HttpOnly: true,
	})
	refreshToken, err := token.IssueToDanniRefreshToken(int(result.ID))
	// TODO: Save the token in the DB
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    refreshToken.Value,
		Path:     "/",
		HttpOnly: true,
	})
	http.Redirect(w, r, "/tasks", http.StatusFound)
}

func (s *authService) RefreshTokenHandler(w http.ResponseWriter, r *http.Request) {
	//TODO: Check that the provided refresh token is valid by querying the database
	var user models.User

	dashboards, err := s.dashboardStorage.List(user.ID)
	if err != nil {
		log.Error("couldn't look up user dashboards")
	}

	projects, err := s.projectStorage.List(user.ID)
	if err != nil {
		log.Error("couldn't look up user dashboards")
	}

	accessToken, err := token.IssueToDanniToken(user, s.config.PrivateJWK, dashboards, projects)
	if err != nil {
		http.Error(w, "couldn't issue refresh token", http.StatusInternalServerError)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     AccessTokenCookieName,
		Value:    accessToken,
		Secure:   true,
		HttpOnly: true,
		SameSite: 2,
	})
}

func (s *authService) ServePublicKey(w http.ResponseWriter, r *http.Request) {
	keyset := jwk.NewSet()
	keyset.Add(s.config.PublicJWK)

	buf, err := json.Marshal(keyset)
	if err != nil {
		log.Error(err)
		http.Error(w, "Failed to marshal key", http.StatusInternalServerError)
		return
	}
	w.Header().Add("Content-Type", "application/json")
	_, err = w.Write(buf)
}

func (s *authService) UserInfoHandler(w http.ResponseWriter, r *http.Request) {
	// Check if cookie is set
	accessTokenCookie, err := r.Cookie(AccessTokenCookieName)
	if err != nil {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}

	userInfo, err := token.ValidateToDanniToken(accessTokenCookie.Value)
	switch err {
	case token.MissingFieldError:
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	case nil:
		break
	default:
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}

	marshalled, err := json.Marshal(userInfo)
	if err != nil {
		http.Error(w, "couldn't marshal token", http.StatusInternalServerError)
		return
	}

	w.Header().Add("Content-Type", "application/json")
	w.Write(marshalled)
}

func (s *authService) LoginHandler(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Unimplemented", http.StatusMethodNotAllowed)
}
