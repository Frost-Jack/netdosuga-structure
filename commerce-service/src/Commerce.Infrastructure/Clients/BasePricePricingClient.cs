using Commerce.Domain.Ports;

namespace Commerce.Infrastructure.Clients;

public sealed class BasePricePricingClient : IPricingClient
{
    public Task<CurrentPrice> GetCurrentPriceAsync(string offerId, long basePriceCents, int available, int capacity, CancellationToken cancellationToken) =>
        Task.FromResult(new CurrentPrice(basePriceCents));

    public Task<PricingQuote> QuoteAsync(string customerId, IReadOnlyList<QuoteLine> lines, string? promoCode, long bonusToSpend, CancellationToken cancellationToken)
    {
        var items = lines
            .Select(x => new PricingQuoteItem(x.OfferId, x.BasePriceCents, x.BasePriceCents * x.Quantity, 0))
            .ToList();
        return Task.FromResult(new PricingQuote(items.Sum(x => x.TotalCents), 0, items));
    }

    public Task CommitAsync(string orderId, string customerId, long totalCents, IReadOnlyList<QuoteLine> lines, CancellationToken cancellationToken) =>
        Task.CompletedTask;

    public Task RevertAsync(string orderId, string customerId, long totalCents, CancellationToken cancellationToken) =>
        Task.CompletedTask;
}
