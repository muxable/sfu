# syntax=docker/dockerfile:1

FROM golang:1.18-alpine

RUN apk update
RUN apk upgrade
RUN apk add ffmpeg-dev gcc musl-dev libsrt-dev

WORKDIR /app

COPY go.* ./

RUN go mod download

COPY . ./

RUN go build -v -o /sfu cmd/main.go

ENV APP_ENV=production

EXPOSE 5000/udp
EXPOSE 1935/tcp
EXPOSE 1935/udp
EXPOSE 7000/tcp

CMD [ "/sfu" ]