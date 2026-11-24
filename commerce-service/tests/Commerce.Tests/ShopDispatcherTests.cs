using Commerce.Application.Shop;
using Commerce.Domain.Ports;
using Xunit;

namespace Commerce.Tests;

public sealed class ShopDispatcherTests
{
    [Fact]
    public async Task Dispatch_WithUnknownOperation_Returns404()
    {
        var dispatcher = new ShopDispatcher([]);

        var outcome = await dispatcher.DispatchAsync(
            new ShopCommand("unknown", string.Empty, "{}", string.Empty),
            new ActorContext("u1", "customer"),
            CancellationToken.None);

        Assert.Equal(404, outcome.Status);
        Assert.Equal("UNKNOWN_OPERATION", outcome.Body["code"]);
    }

    [Fact]
    public async Task Dispatch_WithInvalidJson_Returns400()
    {
        var dispatcher = new ShopDispatcher([]);

        var outcome = await dispatcher.DispatchAsync(
            new ShopCommand("catalog.list", string.Empty, "{broken", string.Empty),
            new ActorContext("u1", "customer"),
            CancellationToken.None);

        Assert.Equal(400, outcome.Status);
        Assert.Equal("INVALID_JSON", outcome.Body["code"]);
    }

    [Fact]
    public async Task Dispatch_WithTooLongId_Returns400()
    {
        var dispatcher = new ShopDispatcher([]);

        var outcome = await dispatcher.DispatchAsync(
            new ShopCommand("catalog.list", new string('x', 129), "{}", string.Empty),
            new ActorContext("u1", "customer"),
            CancellationToken.None);

        Assert.Equal(400, outcome.Status);
        Assert.Equal("INVALID_ID", outcome.Body["code"]);
    }

    [Fact]
    public async Task Dispatch_RoutesToMatchingHandler()
    {
        var handler = new EchoHandler();
        var dispatcher = new ShopDispatcher([handler]);

        var outcome = await dispatcher.DispatchAsync(
            new ShopCommand("orders.list", string.Empty, "{}", string.Empty),
            new ActorContext("u1", "customer"),
            CancellationToken.None);

        Assert.Equal(200, outcome.Status);
        Assert.Equal("u1", outcome.Body["actor"]);
    }

    private sealed class EchoHandler : IRequestHandler
    {
        public bool CanHandle(string operation) => operation == "orders.list";

        public Task<ShopOutcome> HandleAsync(HandlerContext context, CancellationToken cancellationToken) =>
            Task.FromResult(ShopOutcome.Ok(new Dictionary<string, object?> { ["actor"] = context.Actor.Id }));
    }
}
