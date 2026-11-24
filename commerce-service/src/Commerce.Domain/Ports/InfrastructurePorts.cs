namespace Commerce.Domain.Ports;

public sealed record ActorContext(string Id, string Role)
{
    public bool IsAdmin => Role == "admin";
    public bool IsVendor => Role == "vendor";
    public bool IsCustomer => Role == "customer";
}

public sealed record PricingQuoteItem(string OfferId, long UnitPriceCents, long TotalCents, long DiscountCents);

public sealed record PricingQuote(long TotalCents, long DiscountCents, IReadOnlyList<PricingQuoteItem> Items);

public sealed record CurrentPrice(long PriceCents);

public interface IClock
{
    DateTimeOffset UtcNow { get; }
}

public interface IIdentifierGenerator
{
    string NewId();
}

public interface IActorResolver
{
    Task<ActorContext> ResolveAsync(string? authorization, CancellationToken cancellationToken);
}

public interface IPricingClient
{
    Task<CurrentPrice> GetCurrentPriceAsync(string offerId, long basePriceCents, int available, int capacity, CancellationToken cancellationToken);

    Task<PricingQuote> QuoteAsync(string customerId, IReadOnlyList<QuoteLine> lines, string? promoCode, long bonusToSpend, CancellationToken cancellationToken);

    Task CommitAsync(string orderId, string customerId, long totalCents, IReadOnlyList<QuoteLine> lines, CancellationToken cancellationToken);

    Task RevertAsync(string orderId, string customerId, long totalCents, CancellationToken cancellationToken);
}

public sealed record QuoteLine(string OfferId, long BasePriceCents, int Quantity, int Available, int Capacity);

public interface IPaymentClient
{
    Task<string> AuthorizeAsync(string orderId, long amountCents, string scenario, CancellationToken cancellationToken);

    Task<string> RefundAsync(string orderId, long amountCents, string scenario, CancellationToken cancellationToken);

    Task CancelAsync(string orderId, long amountCents, CancellationToken cancellationToken);
}

public interface IUnitOfWork
{
    Task<T> ExecuteAsync<T>(Func<CancellationToken, Task<T>> action, string? customerId = null, CancellationToken cancellationToken = default);
    Task FlushAsync(CancellationToken cancellationToken = default);
}
