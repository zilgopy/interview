# Use the official Go Alpine image as the base image
FROM golang:1.18-alpine

# Install necessary build tools
RUN apk add --no-cache curl

# Set the working directory
WORKDIR /app

# Copy the go.mod and go.sum files into the container
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the source code into the container
COPY main.go ./
COPY swagger.yaml ./

# Build the application
RUN go build -o main .

# Expose the API port
EXPOSE 3000

# Run the application
CMD ["./main"]
