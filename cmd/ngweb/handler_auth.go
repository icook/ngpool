package main

import (
	"database/sql"
	"github.com/dgrijalva/jwt-go"
	"github.com/gin-gonic/gin"
	"github.com/lib/pq"
	"github.com/pkg/errors"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
	"time"
)

var signMethod jwt.SigningMethod = jwt.SigningMethodHS256

// The relams we allow a user to set on a new API key
var UserKeygenRealms = []string{"trade", "withdraw"}

// The relams we give to conventional logins through the web interface
var JWTRealms = []string{"trade", "withdraw", "keygen", "tfa"}

type User struct {
	Username   string
	ID         int
	Password   string
	TFAEnabled bool   `db:"tfa_enabled"`
	TFACode    string `db:"tfa_code"`
}

func (q *NgWebAPI) postTFA(c *gin.Context) {
	type TwoFactoReq struct {
		Code string `validate:"required"`
	}
	var req TwoFactoReq
	if !q.BindValid(c, &req) {
		return
	}
	userID := c.GetInt("userID")
	var user User
	err := q.db.QueryRowx(
		"SELECT tfa_code, tfa_enabled FROM users WHERE id = $1",
		userID).StructScan(&user)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	valid := totp.Validate(req.Code, user.TFACode)
	if !valid {
		q.apiError(c, 400, APIError{
			Code:  "invalid_code",
			Title: "The two factor code provided is not valid"})
		return
	}
	if !user.TFAEnabled {
		res, err := q.db.Exec(`UPDATE users SET tfa_enabled = true WHERE id = $1`, userID)
		if err != nil {
			q.apiException(c, 500, errors.WithStack(err), SQLError)
			return
		}
		affect, err := res.RowsAffected()
		if affect < 1 {
			q.apiException(c, 500, errors.WithStack(err), SQLError)
			return
		}
	}
	tokenString, err := q.createToken(user.Username, userID, JWTRealms)
	q.apiSuccess(c, 200, res{"token": tokenString})
}

func (q *NgWebAPI) postTFASetup(c *gin.Context) {
	userID := c.GetInt("userID")
	user := make(map[string]interface{})
	err := q.db.QueryRowx(
		"SELECT id, email, tfa_enabled FROM users WHERE id = $1",
		userID).MapScan(user)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	if user["tfa_enabled"].(bool) == true {
		q.apiError(c, 400, APIError{
			Code:  "already_setup",
			Title: "Two factor authentication is already setup"})
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "NgWebAPI test",
		AccountName: user["email"].(string),
	})
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), APIError{
			Code:  "keygen_err",
			Title: "Failed to generate new two factor key for unknown reason"})
		return
	}
	result, err := q.db.Exec(
		`UPDATE users SET tfa_code = $1 WHERE id = $2 AND tfa_enabled = false`,
		key.Secret(), userID)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), SQLError)
		return
	}
	affect, err := result.RowsAffected()
	if affect < 1 {
		// Only occurs in edge case
		q.apiError(c, 400, APIError{
			Code:  "already_setup",
			Title: "Two factor authentication is already setup"})
		return
	}
	q.apiSuccess(c, 200, res{"tfa_enroll_string": key.String()})
}

func (q *NgWebAPI) postLogin(c *gin.Context) {
	type LoginReq struct {
		Username string `json:"username" validate:"required"`
		Password string `json:"password" validate:"required"`
	}
	var req LoginReq
	var err error
	if !q.BindValid(c, &req) {
		return
	}
	var user User
	// We select >100000 to only allow login by user accounts, and not internal
	// accounts
	err = q.db.QueryRowx(
		`SELECT id, username, password, tfa_enabled FROM users
		WHERE username = $1 AND id >= 100000`,
		req.Username).StructScan(&user)
	if err != nil {
		if err == sql.ErrNoRows {
			q.apiError(c, 403, APIError{
				Code:  "invalid_auth",
				Title: "Invalid username or password provided"})
		} else {
			q.apiException(c, 500, errors.WithStack(err), SQLError)
		}
		return
	}
	err = bcrypt.CompareHashAndPassword(
		[]byte(user.Password), []byte(req.Password))
	if err != nil {
		q.apiError(c, 403, APIError{
			Code:  "invalid_auth",
			Title: "Invalid username or password provided"})
		return
	}
	// Only given them read access and tfa auth endpoint access if they have
	// tfa enabled. Otherwise full access
	var realms []string
	if user.TFAEnabled {
		realms = []string{"tfa"}
	} else {
		realms = JWTRealms
	}
	tokenString, err := q.createToken(user.Username, user.ID, realms)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), APIError{
			Code:  "tokengen_err",
			Title: "Failed to generate JWT token unknown reason"})
		return
	}
	q.apiSuccess(c, 200, res{
		"user_id":     user.ID,
		"username":    user.Username,
		"token":       tokenString,
		"tfa_enabled": user.TFAEnabled,
	})
}

func (q *NgWebAPI) createToken(username string, id int, realms []string) (string, error) {
	expire := time.Now().Add(time.Hour).Unix()
	token := jwt.NewWithClaims(signMethod, customClaims{
		Username: username,
		UserID:   id,
		StandardClaims: jwt.StandardClaims{
			ExpiresAt: expire,
		},
	})
	tokenString, err := token.SignedString([]byte(q.config.GetString("JWTSecret")))
	if err != nil {
		return "", err
	}
	return tokenString, nil
}

func (q *NgWebAPI) postRegister(c *gin.Context) {
	type RegisterReq struct {
		Username string  `validate:"required,alphanum,lt=32"`
		Email    *string `validate:"omitempty,email" json:"omitempty"`
		Password string  `validate:"required,gt=8,lt=128"`
	}
	var req RegisterReq
	if !q.BindValid(c, &req) {
		return
	}
	bcryptPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), 6)
	if err != nil {
		q.apiException(c, 500, errors.WithStack(err), APIError{
			Code:  "bcrypt_error",
			Title: "Unable to hash password for unknown reason"})
		return
	}
	var userID int
	err = q.db.QueryRow(
		`INSERT INTO users (username, email, password)
		VALUES ($1, $2, $3) RETURNING id`,
		req.Username, req.Email, bcryptPassword).Scan(&userID)
	if err != nil {
		if pqe, ok := err.(*pq.Error); ok && pqe.Code == "23505" {
			if pqe.Constraint == "unique_email" {
				q.apiError(c, 400, APIError{
					Code:  "email_taken",
					Title: "Email address already in use"})
				return
			}
			if pqe.Constraint == "unique_username" {
				q.apiError(c, 400, APIError{
					Code:  "username_taken",
					Title: "Username already in use"})
				return
			}
			q.apiException(c, 500, errors.WithStack(err), SQLError)
		} else {
			q.apiException(c, 500, errors.WithStack(err), SQLError)
		}
		return
	}

	q.apiSuccess(c, 201, res{"id": userID})
}
