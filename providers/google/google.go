// Package google implements the OAuth2 protocol for authenticating users
// through Google.
package google

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strings"

	"github.com/markbates/goth"
	"golang.org/x/oauth2"
)

const (
	endpointProfile string = "https://www.googleapis.com/oauth2/v2/userinfo"
	authURL         string = "https://accounts.google.com/o/oauth2/auth?access_type=offline"
	tokenURL        string = "https://accounts.google.com/o/oauth2/token"
	idTokenProfile  string = "https://www.googleapis.com/oauth2/v3/tokeninfo"
)

// New creates a new Google provider, and sets up important connection details.
// You should always call `google.New` to get a new Provider. Never try to create
// one manually.
func New(clientKey, secret, callbackURL string, scopes ...string) *Provider {
	p := &Provider{
		ClientKey:    clientKey,
		Secret:       secret,
		CallbackURL:  callbackURL,
		providerName: "google",

		// We can get a refresh token from Google by this option.
		// See https://developers.google.com/identity/protocols/oauth2/openid-connect#access-type-param
		authCodeOptions: []oauth2.AuthCodeOption{
			oauth2.AccessTypeOffline,
		},
	}
	p.config = newConfig(p, scopes)
	return p
}

// Provider is the implementation of `goth.Provider` for accessing Google.
type Provider struct {
	ClientKey       string
	Secret          string
	CallbackURL     string
	HTTPClient      *http.Client
	config          *oauth2.Config
	authCodeOptions []oauth2.AuthCodeOption
	providerName    string
}

// Name is the name used to retrieve this provider later.
func (p *Provider) Name() string {
	return p.providerName
}

// SetName is to update the name of the provider (needed in case of multiple providers of 1 type)
func (p *Provider) SetName(name string) {
	p.providerName = name
}

// Client returns an HTTP client to be used in all fetch operations.
func (p *Provider) Client() *http.Client {
	return goth.HTTPClientWithFallBack(p.HTTPClient)
}

// Debug is a no-op for the google package.
func (p *Provider) Debug(debug bool) {}

// BeginAuth asks Google for an authentication endpoint.
func (p *Provider) BeginAuth(state string) (goth.Session, error) {
	url := p.config.AuthCodeURL(state, p.authCodeOptions...)
	session := &Session{
		AuthURL: url,
	}
	return session, nil
}

type googleUser struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	FirstName string `json:"given_name"`
	LastName  string `json:"family_name"`
	Link      string `json:"link"`
	Picture   string `json:"picture"`
}

// FetchUser will go to Google and access basic information about the user.
func (p *Provider) FetchUser(session goth.Session) (goth.User, error) {
	sess := session.(*Session)
	user := goth.User{
		AccessToken:  sess.AccessToken,
		Provider:     p.Name(),
		RefreshToken: sess.RefreshToken,
		ExpiresAt:    sess.ExpiresAt,
		IDToken:      sess.IDToken,
	}

	if user.AccessToken == "" && user.IDToken == "" {
		// Data is not yet retrieved, since accessToken is still empty.
		return user, fmt.Errorf("%s cannot get user information without accessToken AND idToken", p.providerName)
	}

	var response *http.Response
	var err error
	retrievedViaIDToken := false

	if user.IDToken != "" && user.IDToken == user.AccessToken {
		retrievedViaIDToken = true
		response, err = p.Client().Get(idTokenProfile + "?id_token=" + url.QueryEscape(sess.IDToken))
		if response.StatusCode == http.StatusBadRequest && len(sess.AccessToken) > 0 {
			response, err = p.Client().Get(endpointProfile + "?access_token=" + url.QueryEscape(sess.AccessToken))
			retrievedViaIDToken = false
		}

	} else {
		response, err = p.Client().Get(endpointProfile + "?access_token=" + url.QueryEscape(sess.AccessToken))
	}

	if response != nil {
		defer response.Body.Close()
	}

	if err != nil {
		return user, err
	}

	if response.StatusCode != http.StatusOK {
		return user, fmt.Errorf("%s responded with a %d trying to fetch user information", p.providerName, response.StatusCode)
	}

	responseBytes, err := ioutil.ReadAll(response.Body)
	if err != nil {
		return user, err
	}

	var u googleUser

	if err := json.Unmarshal(responseBytes, &u); err != nil {
		return user, err
	}

	// Extract the user data we got from Google into our goth.User.
	if err := json.Unmarshal(responseBytes, &user.RawData); err != nil {
		return user, err
	}

	if retrievedViaIDToken {

		uClaim := struct {
			ID        string `json:"sub"`
			Name      string `json:"name"`
			Email     string `json:"email"`
			FirstName string `json:"given_name"`
			LastName  string `json:"family_name"`
			Picture   string `json:"picture"`
			Verified  string `json:"email_verified"`
			Issuer    string `json:"iis"`
			Audience  string `json:"aud"`
			IssuedAt  string `json:"iat"`
			Expiry    string `json:"exp"`
		}{}

		err := json.Unmarshal(responseBytes, &uClaim)

		if err != nil {
			return user, err
		}

		user.UserID = uClaim.ID
		user.Email = uClaim.Email
		user.Name = uClaim.Name
		user.FirstName = uClaim.FirstName
		user.LastName = uClaim.LastName
		user.AvatarURL = uClaim.Picture
		return user, nil
	}

	user.Name = u.Name
	user.FirstName = u.FirstName
	user.LastName = u.LastName
	user.NickName = u.Name
	user.Email = u.Email
	user.AvatarURL = u.Picture
	user.UserID = u.ID
	// Google provides other useful fields such as 'hd'; get them from RawData

	return user, nil
}

func newConfig(provider *Provider, scopes []string) *oauth2.Config {
	c := &oauth2.Config{
		ClientID:     provider.ClientKey,
		ClientSecret: provider.Secret,
		RedirectURL:  provider.CallbackURL,
		Endpoint:     Endpoint,
		Scopes:       []string{},
	}

	if len(scopes) > 0 {
		c.Scopes = append(c.Scopes, scopes...)
	} else {
		c.Scopes = []string{"email"}
	}
	return c
}

// RefreshTokenAvailable refresh token is provided by auth provider or not
func (p *Provider) RefreshTokenAvailable() bool {
	return true
}

// RefreshToken get new access token based on the refresh token
func (p *Provider) RefreshToken(refreshToken string) (*oauth2.Token, error) {
	token := &oauth2.Token{RefreshToken: refreshToken}
	ts := p.config.TokenSource(goth.ContextForClient(p.Client()), token)
	newToken, err := ts.Token()
	if err != nil {
		return nil, err
	}
	return newToken, err
}

// SetPrompt sets the prompt values for the google OAuth call. Use this to
// force users to choose and account every time by passing "select_account",
// for example.
// See https://developers.google.com/identity/protocols/OpenIDConnect#authenticationuriparameters
func (p *Provider) SetPrompt(prompt ...string) {
	if len(prompt) == 0 {
		return
	}
	p.authCodeOptions = append(p.authCodeOptions, oauth2.SetAuthURLParam("prompt", strings.Join(prompt, " ")))
}

// SetHostedDomain sets the hd parameter for google OAuth call.
// Use this to force user to pick user from specific hosted domain.
// See https://developers.google.com/identity/protocols/oauth2/openid-connect#hd-param
func (p *Provider) SetHostedDomain(hd string) {
	if hd == "" {
		return
	}
	p.authCodeOptions = append(p.authCodeOptions, oauth2.SetAuthURLParam("hd", hd))
}

// SetLoginHint sets the login_hint parameter for the Google OAuth call.
// Use this to prompt the user to log in with a specific account.
// See https://developers.google.com/identity/protocols/oauth2/openid-connect#login-hint
func (p *Provider) SetLoginHint(loginHint string) {
	if loginHint == "" {
		return
	}
	p.authCodeOptions = append(p.authCodeOptions, oauth2.SetAuthURLParam("login_hint", loginHint))
}

// SetAccessType sets the access_type parameter for the Google OAuth call.
// If an access token is being requested, the client does not receive a refresh token unless a value of offline is specified.
// See https://developers.google.com/identity/protocols/oauth2/openid-connect#access-type-param
func (p *Provider) SetAccessType(at string) {
	if at == "" {
		return
	}
	p.authCodeOptions = append(p.authCodeOptions, oauth2.SetAuthURLParam("access_type", at))
}

func (p *Provider) FetchUserWithToken(token string) (goth.User, error) {
	return goth.User{}, errors.New("not implemented")
}

func (p *Provider) GetClientID() (string, error) {
	return p.config.ClientID, nil
}
