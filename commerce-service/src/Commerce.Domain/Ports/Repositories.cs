using Commerce.Domain.Entities;

namespace Commerce.Domain.Ports;

public sealed record VendorSummary(string Id, string Organization, int Applications, int Offers);

public sealed record OfferRating(double AverageRating, int ReviewsCount);

public interface IApplicationRepository
{
    Task<IReadOnlyList<Application>> ListAsync(string actorId, bool includeAll, CancellationToken cancellationToken);

    Task<Application?> GetAsync(string id, CancellationToken cancellationToken);

    void Add(Application application);

    Task<IReadOnlyList<VendorSummary>> ListVendorsAsync(CancellationToken cancellationToken);
}

public interface IOfferRepository
{
    Task<IReadOnlyList<Offer>> ListPublishedAsync(string city, string category, CancellationToken cancellationToken);

    Task<IReadOnlyList<Offer>> ListAsync(string actorId, bool includeAll, CancellationToken cancellationToken);

    Task<Offer?> GetAsync(string id, CancellationToken cancellationToken);

    void Add(Offer offer);

    Task SetPhotoPathAsync(string id, string photoPath, CancellationToken cancellationToken);
}

public interface IOfferRatingReader
{
    Task<OfferRating> GetAsync(string offerId, CancellationToken cancellationToken);
}

public interface ICartRepository
{
    Task<IReadOnlyList<CartItem>> ListAsync(string customerId, CancellationToken cancellationToken);

    Task<CartItem?> GetAsync(string customerId, string offerId, CancellationToken cancellationToken);

    void Upsert(CartItem item);

    void Remove(string customerId, string offerId);

    void Clear(string customerId);
}

public interface IOrderRepository
{
    Task<Order?> GetAsync(string id, CancellationToken cancellationToken);

    Task<Order?> GetByIdempotencyKeyAsync(string customerId, string key, CancellationToken cancellationToken);

    Task<IReadOnlyList<Order>> ListAsync(string actorId, bool includeAll, CancellationToken cancellationToken);

    Task<IReadOnlyList<Order>> ListExpiredAsync(int limit, CancellationToken cancellationToken);

    void Add(Order order);

    void AddItem(OrderItem item);

    Task<IReadOnlyList<OrderItem>> ListItemsAsync(string orderId, CancellationToken cancellationToken);

    void AddTicket(Ticket ticket);

    Task<IReadOnlyList<Ticket>> ListTicketsAsync(string orderId, CancellationToken cancellationToken);

    Task<Ticket?> GetTicketByCodeAsync(string code, CancellationToken cancellationToken);

    Task<bool> HasRedeemedTicketsAsync(string orderId, CancellationToken cancellationToken);

    void RevokeTickets(string orderId);

    Task<IReadOnlyList<OrderItem>> ListSalesAsync(string actorId, bool includeAll, CancellationToken cancellationToken);
}

public interface IOutboxRepository
{
    void Add(OutboxEvent @event);

    Task<IReadOnlyList<OutboxEvent>> ListUnpublishedAsync(int limit, CancellationToken cancellationToken);

    Task MarkPublishedAsync(string eventId, CancellationToken cancellationToken);
}

public interface IAttemptRepository
{
    Task<bool> HasPaymentAttemptAsync(string orderId, CancellationToken cancellationToken);

    Task<long?> GetPaymentAttemptAmountAsync(string orderId, CancellationToken cancellationToken);

    void AddPaymentAttempt(string orderId, long amountCents, DateTimeOffset createdAt);

    Task<RefundAttempt?> GetRefundAttemptAsync(string orderId, CancellationToken cancellationToken);

    Task UpsertRefundAttempt(string orderId, DateTimeOffset createdAt, CancellationToken cancellationToken);

    void DeleteRefundAttempt(string orderId);
}
