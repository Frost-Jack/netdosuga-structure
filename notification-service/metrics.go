package main

import "github.com/prometheus/client_golang/prometheus"

var notificationOperations = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "netdosuga_operations_total", ConstLabels: prometheus.Labels{"service": "notification-worker"}}, []string{"operation", "outcome"})
var notificationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "netdosuga_operation_duration_seconds", ConstLabels: prometheus.Labels{"service": "notification-worker"}}, []string{"operation"})
