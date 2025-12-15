package http

// Controller handle all the request.
type Controller struct {
	biz biz.IBiz
	val *validation.Validator
}

// NewController Create a new Controller instance.
func NewController(biz biz.IBiz, val *validation.Validator) *Controller {
	return &Controller{
		biz: biz,
		val: val,
	}
}
