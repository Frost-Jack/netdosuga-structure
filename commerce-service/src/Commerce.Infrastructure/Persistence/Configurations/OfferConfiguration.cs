using Commerce.Domain;
using Commerce.Domain.Entities;
using Microsoft.EntityFrameworkCore;
using Microsoft.EntityFrameworkCore.Metadata.Builders;

namespace Commerce.Infrastructure.Persistence.Configurations;

internal sealed class OfferConfiguration : IEntityTypeConfiguration<Offer>
{
    public void Configure(EntityTypeBuilder<Offer> builder)
    {
        builder.ToTable("offers");
        builder.HasKey(x => x.Id);
        builder.Property(x => x.Status)
            .HasConversion(v => v.ToString().ToUpperInvariant(), v => Enum.Parse<OfferStatus>(v, true))
            .HasMaxLength(16);
        builder.Property(x => x.VendorId).HasMaxLength(128).IsRequired();
        builder.Property(x => x.Title).HasMaxLength(300).IsRequired();
        builder.Property(x => x.Description).HasMaxLength(300).IsRequired();
        builder.Property(x => x.City).HasMaxLength(300).IsRequired();
        builder.Property(x => x.Category).HasMaxLength(300).IsRequired();
        builder.Property(x => x.Schedule).HasMaxLength(1000).IsRequired();
        builder.Property(x => x.PhotoPath).HasMaxLength(512).IsRequired();
        builder.Property(x => x.PriceCents).HasPrecision(20);
        builder.HasIndex(x => new { x.Status, x.City, x.Category });

        builder.HasData(
            new Offer
            {
                Id = "offer-demo-1",
                VendorId = "vendor-demo",
                Title = "Планетарий",
                Description = "Демонстрационный сеанс",
                City = "Москва",
                Category = "Наука",
                Schedule = "Ежедневно 10:00–20:00",
                PriceCents = 150000,
                Capacity = 100,
                Available = 100,
                Status = OfferStatus.Published,
                CreatedAt = new DateTimeOffset(2026, 1, 1, 0, 0, 0, TimeSpan.Zero)
            },
            new Offer
            {
                Id = "offer-demo-2",
                VendorId = "vendor-demo",
                Title = "Мастерская",
                Description = "Демонстрационный мастер-класс",
                City = "Москва",
                Category = "Творчество",
                Schedule = "Сб–Вс 12:00–18:00",
                PriceCents = 90000,
                Capacity = 50,
                Available = 50,
                Status = OfferStatus.Published,
                CreatedAt = new DateTimeOffset(2026, 1, 1, 0, 0, 0, TimeSpan.Zero)
            },
            new Offer
            {
                Id = "offer-demo-3",
                VendorId = "vendor-two",
                Title = "Верёвочный парк",
                Description = "Демонстрационный маршрут",
                City = "Казань",
                Category = "Спорт",
                Schedule = "Ежедневно 09:00–19:00",
                PriceCents = 120000,
                Capacity = 80,
                Available = 80,
                Status = OfferStatus.Published,
                CreatedAt = new DateTimeOffset(2026, 1, 1, 0, 0, 0, TimeSpan.Zero)
            });
    }
}
