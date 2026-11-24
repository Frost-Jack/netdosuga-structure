using System.Diagnostics;
using System.Text.Json;
using Commerce.Application.Shop;
using Commerce.Api.Observability;
using Commerce.Domain;
using Commerce.Domain.Ports;
using Grpc.Core;
using Netdosuga.Demo.V1;

namespace Commerce.Api.Grpc;

public sealed class ShopGrpcService(
    ShopDispatcher dispatcher,
    IActorResolver resolver,
    ILogger<ShopGrpcService> logger) : OrderService.OrderServiceBase
{
    public override async Task<ShopResponse> Shop(ShopRequest request, ServerCallContext context)
    {
        var operation = Telemetry.OperationName(request.Operation);
        using var activity = Telemetry.Source.StartActivity("operation." + operation, ActivityKind.Server);
        activity?.SetTag("app.request", JsonSerializer.Serialize(new Dictionary<string, object?> { ["operation"] = operation }));
        var started = TimeProvider.System.GetTimestamp();
        ShopOutcome outcome;
        try
        {
            ActorContext actor;
            if (operation == "catalog.list")
            {
                actor = new ActorContext(string.Empty, "public");
            }
            else
            {
                using var authentication = Telemetry.Source.StartActivity("authentication.resolve", ActivityKind.Client);
                actor = await resolver.ResolveAsync(context.RequestHeaders.GetValue("authorization"), context.CancellationToken);
            }

            outcome = await dispatcher.DispatchAsync(
                new ShopCommand(request.Operation, request.Id, request.BodyJson, request.IdempotencyKey),
                actor,
                context.CancellationToken);
        }
        catch (DomainException error)
        {
            outcome = ShopOutcome.Error(error.Error.Status, error.Error.Code);
        }
        catch (OperationCanceledException) when (context.CancellationToken.IsCancellationRequested)
        {
            throw;
        }
        catch (Exception error)
        {
            logger.LogError(error, "Shop operation failed: {Operation}", operation);
            outcome = ShopOutcome.Error(503, "UNAVAILABLE");
        }

        var traceId = Activity.Current?.TraceId.ToString() ?? activity?.TraceId.ToString() ?? string.Empty;
        var body = new Dictionary<string, object?>(outcome.Body)
        {
            ["demo"] = true,
            ["trace_id"] = traceId
        };
        var responseTags = new Dictionary<string, object?> { ["status"] = outcome.Status };
        if (operation == "orders.pay"
            && body.TryGetValue("order", out var orderValue)
            && orderValue is Dictionary<string, object?> order
            && order.TryGetValue("status", out var orderStatus))
        {
            responseTags = new Dictionary<string, object?>
            {
                ["order"] = new Dictionary<string, object?> { ["status"] = orderStatus }
            };
        }

        activity?.SetTag("app.response", JsonSerializer.Serialize(responseTags));
        Telemetry.ShopOperations.WithLabels(operation, outcome.Status.ToString()).Inc();
        logger.LogInformation(
            "shop request completed in {DurationMs} ms with {Status}",
            TimeProvider.System.GetElapsedTime(started).TotalMilliseconds,
            outcome.Status);
        return new ShopResponse
        {
            Status = outcome.Status,
            BodyJson = JsonSerializer.Serialize(body),
            InstanceId = Environment.MachineName
        };
    }

    public override async Task<CreateDemoOrderResponse> CreateDemoOrder(CreateDemoOrderRequest request, ServerCallContext context)
    {
        CreateDemoOrderResponse Accepted() => new()
        {
            Mock = true,
            OrderId = "demo-order-001",
            Status = "DEMO_ACCEPTED",
            EventId = Guid.NewGuid().ToString("D"),
            TraceId = Activity.Current?.TraceId.ToString() ?? string.Empty
        };

        switch (request.Scenario)
        {
            case "success":
                return Accepted();
            case "delay":
                await Task.Delay(TimeSpan.FromMilliseconds(250), context.CancellationToken);
                return Accepted();
            case "error":
                throw new RpcException(new Status(StatusCode.Unknown, "MOCK_PROVIDER_ERROR"));
            case "timeout":
                await Task.Delay(TimeSpan.FromMilliseconds(1500), context.CancellationToken);
                throw new RpcException(new Status(StatusCode.DeadlineExceeded, "MOCK_TIMEOUT"));
            default:
                throw new RpcException(new Status(StatusCode.InvalidArgument, "INVALID_SCENARIO"));
        }
    }
}
