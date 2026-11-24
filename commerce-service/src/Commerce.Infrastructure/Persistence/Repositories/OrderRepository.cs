using Commerce.Domain;
using Commerce.Domain.Entities;
using Commerce.Domain.Ports;
using Microsoft.EntityFrameworkCore;

namespace Commerce.Infrastructure.Persistence.Repositories;

public sealed class OrderRepository(CommerceDbContext db, IClock clock) : IOrderRepository
{
    public async Task<IReadOnlyList<Order>> ListAsync(string actorId, bool includeAll, CancellationToken cancellationToken)
    {
        var items = await db.Orders
            .Where(x => includeAll || x.CustomerId == actorId)
            .OrderByDescending(x => x.CreatedAt)
            .Take(200)
            .ToListAsync(cancellationToken);
        return items;
    }

    public async Task<IReadOnlyList<Order>> ListExpiredAsync(int limit, CancellationToken cancellationToken)
    {
        var now = clock.UtcNow;
        var items = await db.Orders
            .Where(x => x.Status == OrderStatus.Reserved && x.ExpiresAt <= now)
            .OrderBy(x => x.ExpiresAt)
            .Take(limit)
            .ToListAsync(cancellationToken);
        return items;
    }

    public Task<Order?> GetAsync(string id, CancellationToken cancellationToken) =>
        db.Orders.FirstOrDefaultAsync(x => x.Id == id, cancellationToken);

    public Task<Order?> GetByIdempotencyKeyAsync(string customerId, string key, CancellationToken cancellationToken) =>
        db.Orders.FirstOrDefaultAsync(x => x.CustomerId == customerId && x.IdempotencyKey == key, cancellationToken);

    public void Add(Order order) => db.Orders.Add(order);

    public void AddItem(OrderItem item) => db.OrderItems.Add(item);

    public async Task<IReadOnlyList<OrderItem>> ListItemsAsync(string orderId, CancellationToken cancellationToken)
    {
        var items = await db.OrderItems
            .Where(x => x.OrderId == orderId)
            .OrderBy(x => x.OfferId)
            .ToListAsync(cancellationToken);
        return items;
    }

    public void AddTicket(Ticket ticket) => db.Tickets.Add(ticket);

    public async Task<IReadOnlyList<Ticket>> ListTicketsAsync(string orderId, CancellationToken cancellationToken)
    {
        var items = await db.Tickets
            .Where(x => x.OrderId == orderId)
            .OrderBy(x => x.Code)
            .ToListAsync(cancellationToken);
        return items;
    }

    public Task<Ticket?> GetTicketByCodeAsync(string code, CancellationToken cancellationToken) =>
        db.Tickets.FirstOrDefaultAsync(x => x.Code == code, cancellationToken);

    public Task<bool> HasRedeemedTicketsAsync(string orderId, CancellationToken cancellationToken) =>
        db.Tickets.AnyAsync(x => x.OrderId == orderId && x.Status == TicketStatus.Redeemed, cancellationToken);

    public void RevokeTickets(string orderId)
    {
        var tracked = db.Tickets.Local.Where(x => x.OrderId == orderId).ToList();
        foreach (var ticket in tracked)
        {
            ticket.Status = TicketStatus.Revoked;
        }

        foreach (var ticket in db.Tickets.Where(x => x.OrderId == orderId).AsEnumerable().Except(tracked))
        {
            ticket.Status = TicketStatus.Revoked;
        }
    }

    public async Task<IReadOnlyList<OrderItem>> ListSalesAsync(string actorId, bool includeAll, CancellationToken cancellationToken)
    {
        var items = await (from item in db.OrderItems
                           join order in db.Orders on item.OrderId equals order.Id
                           where order.Status == OrderStatus.Paid && (includeAll || item.VendorId == actorId)
                           orderby order.CreatedAt descending
                           select item)
            .Take(500)
            .ToListAsync(cancellationToken);
        return items;
    }
}
