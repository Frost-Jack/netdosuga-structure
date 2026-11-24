using System.Text.Json;
using Commerce.Application.Handlers;
using Commerce.Application.Shop;
using Commerce.Domain;
using Commerce.Domain.Entities;
using Commerce.Domain.Ports;
using Xunit;

namespace Commerce.Tests;

public sealed class OrderHandlerTests
{
    [Fact]
    public async Task Create_ReservesOfferClearsCartAndCalculatesCommission()
    {
        var fixture = new Fixture();
        fixture.Cart.Upsert(new CartItem { CustomerId = "c1", OfferId = "offer-1", Quantity = 2 });

        var outcome = await fixture.Handle("orders.create", "key-1");

        Assert.Equal(200, outcome.Status);
        var order = Assert.IsType<Dictionary<string, object?>>(outcome.Body["order"]);
        Assert.Equal(300000L, order["total_cents"]);
        Assert.Equal(30000L, order["commission_cents"]);
        Assert.Equal(8, fixture.Offer.Available);
        Assert.Empty(await fixture.Cart.ListAsync("c1", CancellationToken.None));
        Assert.Single(fixture.Orders.Items);
        Assert.Single(fixture.Orders.OrderItems);
        Assert.Single(fixture.Events.Events);
    }

    [Fact]
    public async Task Create_WithSameIdempotencyKey_ReturnsExistingOrder()
    {
        var fixture = new Fixture();
        fixture.Cart.Upsert(new CartItem { CustomerId = "c1", OfferId = "offer-1", Quantity = 2 });

        await fixture.Handle("orders.create", "key-1");
        fixture.Cart.Upsert(new CartItem { CustomerId = "c1", OfferId = "offer-1", Quantity = 1 });
        var outcome = await fixture.Handle("orders.create", "key-1");

        Assert.Equal(200, outcome.Status);
        Assert.Equal(8, fixture.Offer.Available);
        Assert.Single(fixture.Orders.Items);
    }

    private sealed class Fixture
    {
        public FakeOrders Orders { get; } = new();
        public FakeCart Cart { get; } = new();
        public FakeOffers Offers { get; } = new();
        public FakeAttempts Attempts { get; } = new();
        public FakePricing Pricing { get; } = new();
        public FakePayment Payment { get; } = new();
        public FakeEvents Events { get; } = new();
        public Offer Offer { get; } = new()
        {
            Id = "offer-1",
            VendorId = "v1",
            Title = "Планетарий",
            Status = OfferStatus.Published,
            PriceCents = 150000,
            Capacity = 10,
            Available = 10
        };

        public Fixture()
        {
            Offers.Items[Offer.Id] = Offer;
        }

        public Task<ShopOutcome> Handle(string operation, string key)
        {
            var handler = new OrderHandler(
                Orders,
                Cart,
                Offers,
                Attempts,
                new FakeUnitOfWork(),
                Pricing,
                Payment,
                Events,
                new FakeClock(),
                new FakeIds());
            using var document = JsonDocument.Parse("{}");
            var command = new ShopCommand(operation, string.Empty, "{}", key);
            var context = new HandlerContext(command, new ActorContext("c1", "customer"), document.RootElement.Clone());
            return handler.HandleAsync(context, CancellationToken.None);
        }
    }

    private sealed class FakeUnitOfWork : IUnitOfWork
    {
        public Task<T> ExecuteAsync<T>(Func<CancellationToken, Task<T>> action, string? customerId = null, CancellationToken cancellationToken = default) =>
            action(cancellationToken);

        public Task FlushAsync(CancellationToken cancellationToken = default) => Task.CompletedTask;
    }

    private sealed class FakeClock : IClock
    {
        public DateTimeOffset UtcNow { get; } = new(2026, 1, 1, 0, 0, 0, TimeSpan.Zero);
    }

    private sealed class FakeIds : IIdentifierGenerator
    {
        private int _next;
        public string NewId() => "id-" + Interlocked.Increment(ref _next);
    }

    private sealed class FakeEvents : IDomainEventWriter
    {
        public List<string> Events { get; } = [];
        public Task WriteAsync(string eventType, string orderId, CancellationToken cancellationToken)
        {
            Events.Add(eventType);
            return Task.CompletedTask;
        }
    }

    private sealed class FakePricing : IPricingClient
    {
        public Task<CurrentPrice> GetCurrentPriceAsync(string offerId, long basePriceCents, int available, int capacity, CancellationToken cancellationToken) =>
            Task.FromResult(new CurrentPrice(basePriceCents));

        public Task<PricingQuote> QuoteAsync(string customerId, IReadOnlyList<QuoteLine> lines, string? promoCode, long bonusToSpend, CancellationToken cancellationToken)
        {
            var items = lines.Select(x => new PricingQuoteItem(x.OfferId, x.BasePriceCents, x.BasePriceCents * x.Quantity, 0)).ToList();
            return Task.FromResult(new PricingQuote(items.Sum(x => x.TotalCents), 0, items));
        }

        public Task CommitAsync(string orderId, string customerId, long totalCents, IReadOnlyList<QuoteLine> lines, CancellationToken cancellationToken) =>
            Task.CompletedTask;

        public Task RevertAsync(string orderId, string customerId, long totalCents, CancellationToken cancellationToken) =>
            Task.CompletedTask;
    }

    private sealed class FakePayment : IPaymentClient
    {
        public Task<string> AuthorizeAsync(string orderId, long amountCents, string scenario, CancellationToken cancellationToken) =>
            Task.FromResult("payment-1");

        public Task<string> RefundAsync(string orderId, long amountCents, string scenario, CancellationToken cancellationToken) =>
            Task.FromResult("refund-1");

        public Task CancelAsync(string orderId, long amountCents, CancellationToken cancellationToken) =>
            Task.CompletedTask;
    }

    private sealed class FakeOrders : IOrderRepository
    {
        public Dictionary<string, Order> Items { get; } = new(StringComparer.Ordinal);
        public List<OrderItem> OrderItems { get; } = [];
        private readonly List<Ticket> _tickets = [];

        public Task<Order?> GetAsync(string id, CancellationToken cancellationToken) =>
            Task.FromResult(Items.TryGetValue(id, out var order) ? order : null);

        public Task<Order?> GetByIdempotencyKeyAsync(string customerId, string key, CancellationToken cancellationToken) =>
            Task.FromResult(Items.Values.FirstOrDefault(x => x.CustomerId == customerId && x.IdempotencyKey == key));

        public Task<IReadOnlyList<Order>> ListAsync(string actorId, bool includeAll, CancellationToken cancellationToken) =>
            Task.FromResult<IReadOnlyList<Order>>(Items.Values.Where(x => includeAll || x.CustomerId == actorId).ToList());

        public Task<IReadOnlyList<Order>> ListExpiredAsync(int limit, CancellationToken cancellationToken) =>
            Task.FromResult<IReadOnlyList<Order>>(Items.Values.Where(x => x.Status == OrderStatus.Reserved && x.ExpiresAt <= DateTimeOffset.UtcNow).Take(limit).ToList());

        public void Add(Order order) => Items[order.Id] = order;
        public void AddItem(OrderItem item) => OrderItems.Add(item);

        public Task<IReadOnlyList<OrderItem>> ListItemsAsync(string orderId, CancellationToken cancellationToken) =>
            Task.FromResult<IReadOnlyList<OrderItem>>(OrderItems.Where(x => x.OrderId == orderId).ToList());

        public void AddTicket(Ticket ticket) => _tickets.Add(ticket);

        public Task<IReadOnlyList<Ticket>> ListTicketsAsync(string orderId, CancellationToken cancellationToken) =>
            Task.FromResult<IReadOnlyList<Ticket>>(_tickets.Where(x => x.OrderId == orderId).ToList());

        public Task<Ticket?> GetTicketByCodeAsync(string code, CancellationToken cancellationToken) =>
            Task.FromResult(_tickets.FirstOrDefault(x => x.Code == code));

        public Task<bool> HasRedeemedTicketsAsync(string orderId, CancellationToken cancellationToken) =>
            Task.FromResult(_tickets.Any(x => x.OrderId == orderId && x.Status == TicketStatus.Redeemed));

        public void RevokeTickets(string orderId)
        {
            foreach (var ticket in _tickets.Where(x => x.OrderId == orderId))
            {
                ticket.Status = TicketStatus.Revoked;
            }
        }

        public Task<IReadOnlyList<OrderItem>> ListSalesAsync(string actorId, bool includeAll, CancellationToken cancellationToken) =>
            Task.FromResult<IReadOnlyList<OrderItem>>(OrderItems.Where(x => includeAll || x.VendorId == actorId).ToList());
    }

    private sealed class FakeCart : ICartRepository
    {
        private readonly List<CartItem> _items = [];

        public Task<IReadOnlyList<CartItem>> ListAsync(string customerId, CancellationToken cancellationToken) =>
            Task.FromResult<IReadOnlyList<CartItem>>(_items.Where(x => x.CustomerId == customerId).ToList());

        public Task<CartItem?> GetAsync(string customerId, string offerId, CancellationToken cancellationToken) =>
            Task.FromResult(_items.FirstOrDefault(x => x.CustomerId == customerId && x.OfferId == offerId));

        public void Upsert(CartItem item)
        {
            var existing = _items.FirstOrDefault(x => x.CustomerId == item.CustomerId && x.OfferId == item.OfferId);
            if (existing is null)
            {
                _items.Add(item);
            }
            else
            {
                existing.Quantity = item.Quantity;
            }
        }

        public void Remove(string customerId, string offerId) =>
            _items.RemoveAll(x => x.CustomerId == customerId && x.OfferId == offerId);

        public void Clear(string customerId) => _items.RemoveAll(x => x.CustomerId == customerId);
    }

    private sealed class FakeOffers : IOfferRepository
    {
        public Dictionary<string, Offer> Items { get; } = new(StringComparer.Ordinal);

        public Task<IReadOnlyList<Offer>> ListPublishedAsync(string city, string category, CancellationToken cancellationToken) =>
            Task.FromResult<IReadOnlyList<Offer>>(Items.Values.Where(x => x.Status == OfferStatus.Published).ToList());

        public Task<IReadOnlyList<Offer>> ListAsync(string actorId, bool includeAll, CancellationToken cancellationToken) =>
            Task.FromResult<IReadOnlyList<Offer>>(Items.Values.Where(x => includeAll || x.VendorId == actorId).ToList());

        public Task<Offer?> GetAsync(string id, CancellationToken cancellationToken) =>
            Task.FromResult(Items.TryGetValue(id, out var offer) ? offer : null);

        public void Add(Offer offer) => Items[offer.Id] = offer;

        public Task SetPhotoPathAsync(string id, string photoPath, CancellationToken cancellationToken)
        {
            if (!Items.TryGetValue(id, out var offer))
            {
                throw new KeyNotFoundException(id);
            }
            offer.PhotoPath = photoPath;
            return Task.CompletedTask;
        }
    }

    private sealed class FakeAttempts : IAttemptRepository
    {
        private readonly Dictionary<string, PaymentAttempt> _payments = new(StringComparer.Ordinal);
        private readonly Dictionary<string, RefundAttempt> _refunds = new(StringComparer.Ordinal);

        public Task<bool> HasPaymentAttemptAsync(string orderId, CancellationToken cancellationToken) =>
            Task.FromResult(_payments.ContainsKey(orderId));

        public Task<long?> GetPaymentAttemptAmountAsync(string orderId, CancellationToken cancellationToken) =>
            Task.FromResult<long?>(_payments.TryGetValue(orderId, out var attempt) ? attempt.AmountCents : null);

        public void AddPaymentAttempt(string orderId, long amountCents, DateTimeOffset createdAt) =>
            _payments[orderId] = new PaymentAttempt { OrderId = orderId, AmountCents = amountCents, CreatedAt = createdAt };

        public Task<RefundAttempt?> GetRefundAttemptAsync(string orderId, CancellationToken cancellationToken) =>
            Task.FromResult(_refunds.TryGetValue(orderId, out var attempt) ? attempt : null);

        public Task UpsertRefundAttempt(string orderId, DateTimeOffset createdAt, CancellationToken cancellationToken)
        {
            if (!_refunds.TryGetValue(orderId, out var attempt))
            {
                attempt = new RefundAttempt { OrderId = orderId };
                _refunds[orderId] = attempt;
            }

            attempt.AttemptCount++;
            attempt.CreatedAt = createdAt;
            return Task.CompletedTask;
        }

        public void DeleteRefundAttempt(string orderId) => _refunds.Remove(orderId);
    }
}
