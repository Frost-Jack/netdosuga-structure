using Commerce.Domain.Entities;
using Commerce.Domain.Ports;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Infrastructure.Persistence.Repositories;

public sealed class CartRepository(CommerceDbContext db) : ICartRepository
{
    public async Task<IReadOnlyList<CartItem>> ListAsync(string customerId, CancellationToken cancellationToken)
    {
        var items = await db.CartItems
            .Where(x => x.CustomerId == customerId)
            .OrderBy(x => x.OfferId)
            .ToListAsync(cancellationToken);
        return items;
    }

    public Task<CartItem?> GetAsync(string customerId, string offerId, CancellationToken cancellationToken) =>
        db.CartItems.FirstOrDefaultAsync(x => x.CustomerId == customerId && x.OfferId == offerId, cancellationToken);

    public void Upsert(CartItem item)
    {
        var existing = db.CartItems.Local.FirstOrDefault(x => x.CustomerId == item.CustomerId && x.OfferId == item.OfferId);
        if (existing is null)
        {
            db.CartItems.Add(item);
            return;
        }

        existing.Quantity = item.Quantity;
    }

    public void Remove(string customerId, string offerId)
    {
        var existing = db.CartItems.Local.FirstOrDefault(x => x.CustomerId == customerId && x.OfferId == offerId);
        if (existing is null)
        {
            var stub = new CartItem { CustomerId = customerId, OfferId = offerId };
            db.CartItems.Attach(stub);
            db.CartItems.Remove(stub);
            return;
        }

        db.CartItems.Remove(existing);
    }

    public void Clear(string customerId)
    {
        var tracked = db.CartItems.Local.Where(x => x.CustomerId == customerId).ToList();
        db.CartItems.RemoveRange(tracked);
    }
}
