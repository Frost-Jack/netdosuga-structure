using Commerce.Domain.Entities;
using Commerce.Domain.Ports;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Infrastructure.Persistence.Repositories;

public sealed class AttemptRepository(CommerceDbContext db) : IAttemptRepository
{
    public Task<bool> HasPaymentAttemptAsync(string orderId, CancellationToken cancellationToken) =>
        db.PaymentAttempts.AnyAsync(x => x.OrderId == orderId, cancellationToken);

    public async Task<long?> GetPaymentAttemptAmountAsync(string orderId, CancellationToken cancellationToken)
    {
        var attempt = await db.PaymentAttempts.FirstOrDefaultAsync(x => x.OrderId == orderId, cancellationToken);
        return attempt?.AmountCents;
    }

    public void AddPaymentAttempt(string orderId, long amountCents, DateTimeOffset createdAt)
    {
        var existing = db.PaymentAttempts.Find(orderId);
        if (existing is null)
        {
            db.PaymentAttempts.Add(new PaymentAttempt { OrderId = orderId, AmountCents = amountCents, CreatedAt = createdAt });
            return;
        }

        existing.AmountCents = amountCents;
        existing.CreatedAt = createdAt;
    }

    public Task<RefundAttempt?> GetRefundAttemptAsync(string orderId, CancellationToken cancellationToken) =>
        db.RefundAttempts.FirstOrDefaultAsync(x => x.OrderId == orderId, cancellationToken);

    public async Task UpsertRefundAttempt(string orderId, DateTimeOffset createdAt, CancellationToken cancellationToken)
    {
        var attempt = await db.RefundAttempts.FirstOrDefaultAsync(x => x.OrderId == orderId, cancellationToken);
        if (attempt is null)
        {
            db.RefundAttempts.Add(new RefundAttempt { OrderId = orderId, AttemptCount = 1, CreatedAt = createdAt });
            return;
        }

        attempt.AttemptCount++;
        attempt.CreatedAt = createdAt;
    }

    public void DeleteRefundAttempt(string orderId)
    {
        var attempt = db.RefundAttempts.Local.FirstOrDefault(x => x.OrderId == orderId);
        if (attempt is not null)
        {
            db.RefundAttempts.Remove(attempt);
            return;
        }

        var stub = new RefundAttempt { OrderId = orderId };
        db.RefundAttempts.Attach(stub);
        db.RefundAttempts.Remove(stub);
    }
}
