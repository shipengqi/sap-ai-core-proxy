package grpc

// Controller handle all the requests of gRPC.
type Controller struct {
	apiv1.UnimplementedMiniBlogServer

	biz biz.IBiz
}

// NewController Create a new Controller instance.
func NewController(biz biz.IBiz) *Controller {
	return &Controller{
		biz: biz,
	}
}
