package main

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"gopkg.in/go-playground/validator.v9"
	"io/ioutil"
)

type APIError struct {
	Code   string `json:"code,omitempty"`
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
	Source string `json:"source,omitempty"`
}

var SQLError = APIError{Code: "database_error", Title: "Unknown database exception"}

type stackTracer interface {
	StackTrace() errors.StackTrace
}

func (q *NgWebAPI) apiException(c *gin.Context, status int, err error, errors ...APIError) {
	logCtx := q.log.New("err", err)
	if err, ok := err.(stackTracer); ok {
		frame := err.StackTrace()[0]
		logCtx = logCtx.New("origin", fmt.Sprintf("%v [%n]", frame, frame))
	}
	res, _ := ioutil.ReadAll(c.Request.Body)
	logCtx.New("uri", c.Request.RequestURI, "body", res).Warn("Error in handler")
	q.apiError(c, status, errors...)
}

func (q *NgWebAPI) apiError(c *gin.Context, status int, errors ...APIError) {
	c.JSON(status, gin.H{"errors": errors})
}

type res map[string]interface{}

func (q *NgWebAPI) apiSuccess(c *gin.Context, status int, data interface{}) {
	c.JSON(status, gin.H{"data": data})
}

func (q *NgWebAPI) BindValid(c *gin.Context, output interface{}) bool {
	var err error
	err = c.BindJSON(&output)
	if err != nil {
		q.apiError(c, 400, APIError{
			Code:  "invalid_format",
			Title: "JSON payload format doesn't match expectation"})
		return false
	}
	err = validate.Struct(output)
	if err != nil {
		if vde, ok := err.(validator.ValidationErrors); ok {
			var errors []APIError
			for _, fe := range vde {
				errors = append(errors, APIError{
					Code:   "invalid_field",
					Source: fe.Field(),
					Title:  fmt.Sprintf("Validator '%s' failed on attribute '%s'", fe.Tag(), fe.Field()),
				})
			}
			c.JSON(400, gin.H{"errors": errors})
			return false
		}
		q.apiException(c, 500, errors.WithStack(err), APIError{
			Code:  "unk_validation",
			Title: "An unexpected validation error has occurred",
		})
		return false
	}
	return true
}
