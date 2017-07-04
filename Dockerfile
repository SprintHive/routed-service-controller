FROM golang:1.8.3

RUN curl https://glide.sh/get | sh

ADD . /go/src/github.com/SprintHive/routed-service-controller
WORKDIR /go/src/github.com/SprintHive/routed-service-controller
RUN glide install
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o /routed-service-controller .

ENTRYPOINT ["/routed-service-controller", "-alsologtostderr"]
