using System.Diagnostics;
using System.Net;
using System.Text;
using System.Text.Json;
using Commerce.Domain;
using Commerce.Domain.Ports;
using Commerce.Infrastructure.Observability;

namespace Commerce.Infrastructure.Clients;

public sealed class PaymentMockClient(HttpClient http, string baseUrl) : IPaymentClient
{
    public Task<string> AuthorizeAsync(string orderId, long amountCents, string scenario, CancellationToken cancellationToken) =>
        SendAsync("/v1/payments", "payment", orderId, amountCents, scenario, true, "payment.authorize", cancellationToken);

    public Task<string> RefundAsync(string orderId, long amountCents, string scenario, CancellationToken cancellationToken) =>
        SendAsync("/v1/refunds", "refund", orderId, amountCents, scenario, false, "payment.refund", cancellationToken);

    public async Task CancelAsync(string orderId, long amountCents, CancellationToken cancellationToken)
    {
        var payload = JsonSerializer.Serialize(new { amount_cents = amountCents });
        using var request = new HttpRequestMessage(HttpMethod.Post, baseUrl.TrimEnd('/') + "/v1/payments/" + orderId + "/cancel")
        {
            Content = new StringContent(payload, Encoding.UTF8, "application/json")
        };
        request.Headers.TryAddWithoutValidation("Idempotency-Key", "refund:" + orderId);

        using var response = await SendAsync(request, "payment.reconcile", cancellationToken);
        if (response.StatusCode != HttpStatusCode.OK)
        {
            throw new DomainException(new DomainError(502, "RECONCILIATION_FAILED"));
        }

        using var document = await ParseAsync(response, cancellationToken);
        var root = document.RootElement;
        if (!root.GetProperty("mock").GetBoolean()
            || root.GetProperty("status").GetString() != "SUCCEEDED"
            || root.GetProperty("order_id").GetString() != orderId
            || root.GetProperty("amount_cents").GetInt64() != amountCents
            || !root.GetProperty("cancelled").GetBoolean())
        {
            throw new DomainException(new DomainError(502, "RECONCILIATION_FAILED"));
        }
    }

    private async Task<string> SendAsync(string path, string kind, string orderId, long amountCents, string scenario, bool currency, string operation, CancellationToken cancellationToken)
    {
        var payload = currency
            ? JsonSerializer.Serialize(new { order_id = orderId, amount_cents = amountCents, currency = "RUB", scenario })
            : JsonSerializer.Serialize(new { order_id = orderId, amount_cents = amountCents, scenario });
        using var request = new HttpRequestMessage(HttpMethod.Post, baseUrl.TrimEnd('/') + path)
        {
            Content = new StringContent(payload, Encoding.UTF8, "application/json")
        };
        request.Headers.TryAddWithoutValidation("Idempotency-Key", kind + ":" + orderId);

        using var response = await SendAsync(request, operation, cancellationToken);
        if (kind == "refund" && response.StatusCode == HttpStatusCode.ServiceUnavailable)
        {
            throw new DomainException(new DomainError(502, "MOCK_DECLINED"));
        }

        if (response.StatusCode != HttpStatusCode.OK)
        {
            throw new DomainException(new DomainError(502, kind == "refund" ? "MOCK_FAILED" : "MOCK_FAILED"));
        }

        using var document = await ParseAsync(response, cancellationToken);
        var root = document.RootElement;
        var resultId = kind == "payment" ? "payment_id" : "refund_id";
        if (!root.GetProperty("mock").GetBoolean()
            || root.GetProperty("status").GetString() != "SUCCEEDED"
            || root.GetProperty("order_id").GetString() != orderId
            || root.GetProperty("amount_cents").GetInt64() != amountCents
            || !root.TryGetProperty(resultId, out var idElement)
            || string.IsNullOrEmpty(idElement.GetString()))
        {
            throw new DomainException(new DomainError(502, "MOCK_FAILED"));
        }

        return idElement.GetString()!;
    }

    private async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, string operation, CancellationToken cancellationToken)
    {
        using var activity = InfrastructureTelemetry.Source.StartActivity(operation, ActivityKind.Client);
        try
        {
            return await http.SendAsync(request, cancellationToken);
        }
        catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
        {
            throw new DomainException(DomainError.Timeout("MOCK_TIMEOUT"));
        }
        catch (HttpRequestException)
        {
            throw new DomainException(new DomainError(502, "MOCK_FAILED"));
        }
    }

    private static async Task<JsonDocument> ParseAsync(HttpResponseMessage response, CancellationToken cancellationToken)
    {
        try
        {
            return await JsonDocument.ParseAsync(await response.Content.ReadAsStreamAsync(cancellationToken), cancellationToken: cancellationToken);
        }
        catch (JsonException)
        {
            throw new DomainException(new DomainError(502, "MOCK_FAILED"));
        }
    }
}
