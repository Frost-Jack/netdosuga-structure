package main

import "github.com/prometheus/client_golang/prometheus"

var businessOperations = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "netdosuga_operations_total", ConstLabels: prometheus.Labels{"service": "payment-mock"}}, []string{"operation", "outcome"})
var businessDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "netdosuga_operation_duration_seconds", ConstLabels: prometheus.Labels{"service": "payment-mock"}}, []string{"operation"})
